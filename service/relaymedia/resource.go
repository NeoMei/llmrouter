package relaymedia

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"strconv"
	"strings"
	"time"
)

func (s *Service) grant(r model.RelayMediaResource, expires int64) string {
	h := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(h, "relay-media-v1\x00%s\x00%d\x00%d\x00%s", r.ID, expires, r.Size, r.SHA256)
	return strconv.FormatInt(expires, 10) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// ReadGranted verifies the ready row and capability, then returns an owned,
// bounded, hash-verified immutable buffer. No object-store path escapes. Already
// acquired buffers may finish streaming after revoke; later acquisitions fail.
func (s *Service) ReadGranted(ctx context.Context, id, token string) ([]byte, error) {
	if s == nil || len(id) != 36 || len(token) > 64 {
		return nil, ErrUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrUnavailable
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var snapshot model.RelayMediaResource
	err = model.ReadRelayMedia(ctx, s.db, id, func(r model.RelayMediaResource) error {
		if !s.validGrant(r, expires, token) {
			return ErrUnavailable
		}
		snapshot = r
		return nil
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	// Object-store I/O and hashing never retain a SQL transaction or row lock.
	data, err := s.readObject(ctx, snapshot)
	if err != nil {
		return nil, ErrUnavailable
	}
	// Successful final authorization is the acquisition boundary. Revocation,
	// expiry, deletion or metadata changes during I/O release no bytes.
	err = model.ReadRelayMedia(ctx, s.db, id, func(r model.RelayMediaResource) error {
		if r != snapshot || !s.validGrant(r, expires, token) {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}

func (s *Service) ReadOwned(ctx context.Context, id string, userID, tokenID int) ([]byte, error) {
	if s == nil || len(id) != 36 || userID < 1 || tokenID < 1 {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var snapshot model.RelayMediaResource
	err := model.ReadRelayMedia(ctx, s.db, id, func(r model.RelayMediaResource) error {
		if r.UserID != userID || r.TokenID != tokenID || r.State != "ready" || r.ExpiresAt <= s.now().Unix() {
			return ErrUnavailable
		}
		snapshot = r
		return nil
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	data, err := s.readObject(ctx, snapshot)
	if err != nil {
		return nil, ErrUnavailable
	}
	err = model.ReadRelayMedia(ctx, s.db, id, func(r model.RelayMediaResource) error {
		if r != snapshot {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}

func (s *Service) validGrant(r model.RelayMediaResource, expires int64, token string) bool {
	now := s.now().Unix()
	return r.State == "ready" && r.ExpiresAt > now && expires > now && expires <= r.ExpiresAt && expires <= now+int64(GrantTTL/time.Second) && hmac.Equal([]byte(s.grant(r, expires)), []byte(token))
}
