package relaymedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"time"
)

const Retention = time.Hour
const GrantTTL = 15 * time.Minute
const FetchTimeout = 30 * time.Second

var ErrUnavailable = errors.New("relay media preparation unavailable")
var ErrInvalidInput = errors.New("invalid relay media input")

// PrepareInput is internal: caller must authenticate IDs and supply its own
// opaque request association. No caller metadata, object keys or facts accepted.
type PrepareInput struct {
	UserID, TokenID      int
	RequestID, SourceURL string
}

// Prepared is request-private. Never serialize URL in embeddings/catalog/logs.
// Reuse it across attempts; cancel all supplier work by AttemptDeadline, then
// defer Revoke with the authenticated IDs (including on failure/cancellation).
type Prepared struct {
	ID, URL                         string
	Facts                           VideoFacts
	SHA256, ContentType             string
	Size                            int64
	GrantExpiresAt, AttemptDeadline time.Time
}

type Service struct {
	db      *gorm.DB
	objects objectstore.Store
	origin  string
	secret  []byte
	now     func() time.Time
}

func (s *Service) PrepareMP4(ctx context.Context, in PrepareInput) (Prepared, error) {
	return s.prepareMP4(ctx, in, 0, 0)
}

func (s *Service) PrepareMP4WithResolution(ctx context.Context, in PrepareInput, width, height int) (Prepared, error) {
	return s.prepareMP4(ctx, in, width, height)
}

func (s *Service) prepareMP4(ctx context.Context, in PrepareInput, expectedWidth, expectedHeight int) (Prepared, error) {
	if s == nil {
		return Prepared{}, ErrUnavailable
	}
	if in.UserID <= 0 || in.TokenID <= 0 || len(in.RequestID) < 1 || len(in.RequestID) > 128 {
		return Prepared{}, ErrInvalidInput
	}
	if _, err := uuid.Parse(in.RequestID); err != nil {
		return Prepared{}, ErrInvalidInput
	}
	var existing model.RelayMediaResource
	existingErr := s.db.WithContext(ctx).Where("request_id = ?", in.RequestID).First(&existing).Error
	if existingErr == nil {
		if existing.UserID != in.UserID || existing.TokenID != in.TokenID || existing.State != "ready" || existing.ExpiresAt <= s.now().Unix() {
			return Prepared{}, ErrUnavailable
		}
		stored, readErr := s.readObject(ctx, existing)
		if readErr != nil {
			return Prepared{}, ErrUnavailable
		}
		storedFacts, inspectErr := InspectMP4(stored)
		if inspectErr != nil || !expectedVideoDimensions(expectedWidth, expectedHeight, storedFacts) {
			return Prepared{}, ErrUnavailable
		}
		expires := min(s.now().Add(GrantTTL).Unix(), existing.ExpiresAt)
		return Prepared{ID: existing.ID, URL: s.resourceURL(existing, expires), Facts: storedFacts, SHA256: existing.SHA256, ContentType: "video/mp4", Size: existing.Size, GrantExpiresAt: time.Unix(expires, 0), AttemptDeadline: time.Unix(expires, 0).Add(-30 * time.Second)}, nil
	}
	if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
		return Prepared{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	u, err := url.Parse(in.SourceURL)
	if err != nil || !validSource(u) {
		return Prepared{}, ErrInvalidInput
	}
	base := service.GetSSRFProtectedHTTPClient()
	if base == nil {
		return Prepared{}, ErrUnavailable
	}
	client := *base
	client.Jar = nil
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !validSource(req.URL) || len(via) >= 10 {
			return ErrInvalidInput
		}
		if base.CheckRedirect != nil {
			return base.CheckRedirect(req, via)
		}
		return service.ValidateSSRFProtectedFetchURL(req.URL.String())
	}
	if err = service.ValidateSSRFProtectedFetchURL(u.String()); err != nil {
		return Prepared{}, ErrInvalidInput
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Prepared{}, ErrInvalidInput
	}
	resp, err := client.Do(req)
	if err != nil {
		return Prepared{}, ErrInvalidInput
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength > MaxBytes {
		return Prepared{}, ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return Prepared{}, ErrInvalidInput
	}
	facts, err := InspectMP4(data)
	if err != nil {
		return Prepared{}, err
	}
	if !expectedVideoDimensions(expectedWidth, expectedHeight, facts) {
		return Prepared{}, ErrInvalidInput
	}
	now := s.now()
	sum := sha256.Sum256(data)
	r := model.RelayMediaResource{ID: uuid.NewString(), UserID: in.UserID, TokenID: in.TokenID, RequestID: in.RequestID, Format: facts.Format, Size: facts.Bytes, SHA256: hex.EncodeToString(sum[:]), Width: facts.Width, Height: facts.Height, Seconds: facts.Seconds, AudioKnown: facts.AudioKnown, HasAudio: facts.HasAudio, State: "staging", ExpiresAt: now.Add(Retention).Unix(), CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	r.ObjectKey = "relay-media/" + r.ID
	if err = s.db.WithContext(ctx).Create(&r).Error; err != nil {
		return Prepared{}, ErrUnavailable
	}
	info, err := s.objects.Put(ctx, r.ObjectKey, bytes.NewReader(data), objectstore.PutOptions{ContentType: "video/mp4", Size: r.Size, ChecksumSHA256: r.SHA256})
	if err != nil || info.Size != r.Size {
		return Prepared{}, ErrUnavailable
	}
	// Validate actual stored bytes before publication, including stores that return
	// success incorrectly. A failed/uncertain write always retains its staging row.
	stored, err := s.readObject(ctx, r)
	if err != nil || !bytes.Equal(stored, data) {
		return Prepared{}, ErrUnavailable
	}
	if err = model.RelayMediaReady(ctx, s.db, r.ID, s.now().Unix()); err != nil {
		return Prepared{}, ErrUnavailable
	}
	expires := min(s.now().Add(GrantTTL).Unix(), r.ExpiresAt)
	return Prepared{ID: r.ID, URL: s.resourceURL(r, expires), Facts: facts, SHA256: r.SHA256, ContentType: "video/mp4", Size: r.Size, GrantExpiresAt: time.Unix(expires, 0), AttemptDeadline: time.Unix(expires, 0).Add(-30 * time.Second)}, nil
}

func expectedVideoDimensions(width, height int, facts VideoFacts) bool {
	return width == 0 || (facts.Width == width && facts.Height == height)
}

func (s *Service) resourceURL(r model.RelayMediaResource, expires int64) string {
	return s.origin + "/api/relay-media/resources/" + r.ID + "?token=" + url.QueryEscape(s.grant(r, expires))
}
func validSource(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == ""
}
func (s *Service) readObject(ctx context.Context, r model.RelayMediaResource) ([]byte, error) {
	if r.Size < 1 || r.Size > MaxBytes || r.Format != "mp4" {
		return nil, ErrUnavailable
	}
	body, info, err := s.objects.Open(ctx, r.ObjectKey)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer body.Close()
	if info.Size != r.Size {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(body, r.Size+1))
	if err != nil || int64(len(data)) != r.Size {
		return nil, ErrUnavailable
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != r.SHA256 {
		return nil, ErrUnavailable
	}
	return data, nil
}

// Revoke denies future opens. Use a fresh bounded cleanup context when the
// request was canceled. Deletion is retried by the runtime, never supplier work.
func (s *Service) Revoke(ctx context.Context, id string, userID, tokenID int) error {
	if s == nil {
		return ErrUnavailable
	}
	return model.RevokeRelayMedia(ctx, s.db, id, userID, tokenID)
}
func (s *Service) Purge(ctx context.Context, limit int) error {
	return model.PurgeRelayMedia(ctx, s.db, s.now().Unix(), limit, s.objects.Delete)
}
