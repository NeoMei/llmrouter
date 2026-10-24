package relaymedia

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixtureService(t *testing.T) (*Service, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/h264.mp4")
	require.NoError(t, err)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RelayMediaResource{}))
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sql.Close()) })
	store, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	s, err := NewService(db, Config{PublicOrigin: "https://media.example", GrantSecret: bytes.Repeat([]byte{42}, 32), Objects: store})
	require.NoError(t, err)
	setting := system_setting.GetFetchSetting()
	old := *setting
	*setting = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
	t.Cleanup(func() { *setting = old })
	service.InitHttpClient()
	return s, data
}
func prepareFixture(t *testing.T, s *Service, data []byte) Prepared {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	p, err := s.PrepareMP4(context.Background(), PrepareInput{UserID: 11, TokenID: 22, RequestID: uuid.NewString(), SourceURL: server.URL})
	require.NoError(t, err)
	return p
}
func tokenOf(t *testing.T, p Prepared) string {
	t.Helper()
	u, err := url.Parse(p.URL)
	require.NoError(t, err)
	return u.Query().Get("token")
}
func TestRelayMediaImmutableGrantAndRestart(t *testing.T) {
	s, data := fixtureService(t)
	var changed atomic.Bool
	var fetches atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("Cookie"))
		if changed.Load() {
			_, _ = w.Write([]byte("changed"))
			return
		}
		_, _ = w.Write(data)
	}))
	defer origin.Close()
	p, err := s.PrepareMP4(context.Background(), PrepareInput{11, 22, uuid.NewString(), origin.URL + "/mutable.mp4?private=transient"})
	require.NoError(t, err)
	changed.Store(true)
	require.Equal(t, GrantTTL-30*time.Second, p.AttemptDeadline.Sub(p.GrantExpiresAt.Add(-GrantTTL)))
	token := tokenOf(t, p)
	for i := 0; i < 2; i++ {
		got, err := s.ReadGranted(context.Background(), p.ID, token)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	}
	assert.EqualValues(t, 1, fetches.Load())
	reopenedDB, err := gorm.Open(s.db.Dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	reopenedSQL, err := reopenedDB.DB()
	require.NoError(t, err)
	defer reopenedSQL.Close()
	restarted, err := NewService(reopenedDB, Config{PublicOrigin: s.origin, GrantSecret: s.secret, Objects: s.objects})
	require.NoError(t, err)
	got, err := restarted.ReadGranted(context.Background(), p.ID, token)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	require.Error(t, s.Revoke(context.Background(), p.ID, 12, 22))
	require.Error(t, s.Revoke(context.Background(), p.ID, 11, 23))
	_, err = s.ReadGranted(context.Background(), p.ID, token)
	require.NoError(t, err)
	for _, bad := range []string{token + "x", strings.Replace(token, ".", ".A", 1), "0.x", "", "999999999999999999999999.x"} {
		got, err := s.ReadGranted(context.Background(), p.ID, bad)
		require.Error(t, err)
		assert.Empty(t, got)
	}
	got, err = s.ReadGranted(context.Background(), uuid.NewString(), token)
	require.Error(t, err)
	assert.Empty(t, got)
	require.NoError(t, s.Revoke(context.Background(), p.ID, 11, 22))
	got, err = s.ReadGranted(context.Background(), p.ID, token)
	require.Error(t, err)
	assert.Empty(t, got)
	require.NoError(t, s.Purge(context.Background(), 100))
	var n int64
	require.NoError(t, s.db.Model(&model.RelayMediaResource{}).Count(&n).Error)
	assert.Zero(t, n)
}
func TestRelayMediaFetchBoundaries(t *testing.T) {
	s, data := fixtureService(t)
	for _, kind := range []string{"invalid", "oversized-header", "oversized-chunked", "oversized-gzip", "redirect-credentials", "redirect-scheme", "canceled", "private-denied"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch kind {
				case "invalid":
					_, _ = w.Write([]byte("MP4 fake"))
				case "oversized-header":
					w.Header().Set("Content-Length", "50000001")
					w.WriteHeader(200)
				case "oversized-chunked":
					w.(http.Flusher).Flush()
					_, _ = io.CopyN(w, zeroReader{}, MaxBytes+1)
				case "oversized-gzip":
					w.Header().Set("Content-Encoding", "gzip")
					z := gzip.NewWriter(w)
					_, _ = io.CopyN(z, zeroReader{}, MaxBytes+1)
					_ = z.Close()
				case "redirect-credentials":
					http.Redirect(w, r, "http://secret:password@"+r.Host+"/target", 302)
				case "redirect-scheme":
					http.Redirect(w, r, "file:///etc/passwd", 302)
				case "canceled":
					<-r.Context().Done()
				default:
					_, _ = w.Write(data)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			if kind == "private-denied" {
				system_setting.GetFetchSetting().AllowPrivateIp = false
				defer func() { system_setting.GetFetchSetting().AllowPrivateIp = true }()
			}
			p, err := s.PrepareMP4(ctx, PrepareInput{11, 22, uuid.NewString(), server.URL})
			require.Error(t, err)
			assert.Empty(t, p.URL)
			if kind == "redirect-credentials" || kind == "redirect-scheme" {
				assert.EqualValues(t, 1, requests.Load())
			}
			if kind == "private-denied" || kind == "canceled" {
				assert.Zero(t, requests.Load())
			}
			var n int64
			require.NoError(t, s.db.Model(&model.RelayMediaResource{}).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
	for _, raw := range []string{"http://user:secret@example.com/video", "file:///file", "data:video/mp4,abc", "https://example.com/a#fragment"} {
		_, err := s.PrepareMP4(context.Background(), PrepareInput{11, 22, uuid.NewString(), raw})
		require.Error(t, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type faultStore struct {
	objectstore.Store
	put, remove bool
}

func (s *faultStore) Put(ctx context.Context, k string, r io.Reader, o objectstore.PutOptions) (objectstore.ObjectInfo, error) {
	info, err := s.Store.Put(ctx, k, r, o)
	if s.put {
		return info, errors.New("uncertain Put")
	}
	return info, err
}
func (s *faultStore) Delete(ctx context.Context, k string) error {
	if s.remove {
		return errors.New("Delete unavailable")
	}
	return s.Store.Delete(ctx, k)
}
func TestRelayMediaFaultRecovery(t *testing.T) {
	for _, phase := range []string{"put", "ready-before", "ready-after"} {
		t.Run(phase, func(t *testing.T) {
			s, data := fixtureService(t)
			fault := &faultStore{Store: s.objects, put: phase == "put"}
			s.objects = fault
			callback := "relay_media_fault"
			if phase != "put" {
				position := s.db.Callback().Update().Before("gorm:update")
				if phase == "ready-after" {
					position = s.db.Callback().Update().After("gorm:commit_or_rollback_transaction")
				}
				require.NoError(t, position.Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "relay_media_resources" {
						tx.AddError(errors.New("activation acknowledgement failed"))
					}
				}))
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
			defer source.Close()
			p, err := s.PrepareMP4(context.Background(), PrepareInput{11, 22, uuid.NewString(), source.URL})
			require.Error(t, err)
			assert.Empty(t, p.ID)
			if phase != "put" {
				require.NoError(t, s.db.Callback().Update().Remove(callback))
			}
			var rows []model.RelayMediaResource
			require.NoError(t, s.db.Find(&rows).Error)
			require.Len(t, rows, 1)
			r := rows[0]
			if phase == "ready-after" {
				assert.Equal(t, "ready", r.State)
			} else {
				assert.Equal(t, "staging", r.State)
				got, err := s.ReadGranted(context.Background(), r.ID, s.grant(r, s.now().Add(GrantTTL).Unix()))
				require.Error(t, err)
				assert.Empty(t, got)
			}
			// No blind deletion on an uncertain activation. Both staging and possibly
			// ready resources remain tracked, and expire before storage deletion.
			body, _, err := fault.Open(context.Background(), r.ObjectKey)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			future := time.Unix(r.ExpiresAt+1, 0)
			s.now = func() time.Time { return future }
			fault.remove = true
			require.Error(t, s.Purge(context.Background(), 100))
			require.NoError(t, s.db.First(&r, "id = ?", r.ID).Error)
			assert.Equal(t, "revoked", r.State)
			fault.remove = false
			require.NoError(t, s.Purge(context.Background(), 100))
			require.ErrorIs(t, s.db.First(&r, "id = ?", r.ID).Error, gorm.ErrRecordNotFound)
			_, _, err = fault.Open(context.Background(), r.ObjectKey)
			require.Error(t, err)
		})
	}
}
func TestRelayMediaExpiredAndChangedObject(t *testing.T) {
	s, data := fixtureService(t)
	p := prepareFixture(t, s, data)
	var r model.RelayMediaResource
	require.NoError(t, s.db.First(&r, "id = ?", p.ID).Error)
	// A valid grant cannot expose replacement content, even with identical size.
	changed := bytes.Clone(data)
	changed[len(changed)-1] ^= 1
	_, err := s.objects.Put(context.Background(), r.ObjectKey, bytes.NewReader(changed), objectstore.PutOptions{Size: r.Size, ContentType: "video/mp4"})
	require.NoError(t, err)
	got, err := s.ReadGranted(context.Background(), p.ID, tokenOf(t, p))
	require.Error(t, err)
	assert.Empty(t, got)
	s.now = func() time.Time { return p.GrantExpiresAt }
	got, err = s.ReadGranted(context.Background(), p.ID, tokenOf(t, p))
	require.Error(t, err)
	assert.Empty(t, got)
}

func TestRelayMediaFetchCancellationInFlight(t *testing.T) {
	s, _ := fixtureService(t)
	entered := make(chan struct{})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.PrepareMP4(ctx, PrepareInput{11, 22, uuid.NewString(), source.URL}); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("protected request never reached fixture")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not honor cancellation")
	}
	var n int64
	require.NoError(t, s.db.Model(&model.RelayMediaResource{}).Count(&n).Error)
	assert.Zero(t, n)
}
