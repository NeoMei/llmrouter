package relaymedia

import (
	"bytes"
	"context"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRelayMediaRuntimeConfig(t *testing.T) {
	s, _ := fixtureService(t)
	t.Setenv("RELAY_MEDIA_PUBLIC_ORIGIN", "")
	t.Setenv("RELAY_MEDIA_GRANT_SECRET", "")
	r, err := RuntimeFromEnv(s.db)
	require.NoError(t, err)
	assert.Nil(t, r)
	t.Setenv("RELAY_MEDIA_PUBLIC_ORIGIN", "https://media.example")
	_, err = RuntimeFromEnv(s.db)
	require.Error(t, err)
	for _, origin := range []string{"http://media.example", "https://user:password@media.example", "https://media.example/path", "https://media.example?token=secret", "https://media.example#x"} {
		_, err := NewService(s.db, Config{PublicOrigin: origin, GrantSecret: s.secret, Objects: s.objects})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "password")
	}
	_, err = NewService(s.db, Config{PublicOrigin: "http://127.0.0.1:1234", GrantSecret: s.secret, Objects: s.objects})
	require.Error(t, err)
	_, err = NewService(s.db, Config{PublicOrigin: "http://127.0.0.1:1234", GrantSecret: s.secret, Objects: s.objects, AllowLoopbackHTTP: true})
	require.NoError(t, err)
	_, err = NewService(s.db, Config{PublicOrigin: s.origin, GrantSecret: bytes.Repeat([]byte{1}, 31), Objects: s.objects})
	require.Error(t, err)
}

type blockingDeleteStore struct {
	faultStore
	entered chan struct{}
}

func (s *blockingDeleteStore) Delete(ctx context.Context, key string) error {
	close(s.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestRelayMediaRuntimeCloseAndStartupRecovery(t *testing.T) {
	s, data := fixtureService(t)
	p := prepareFixture(t, s, data)
	require.NoError(t, s.Revoke(context.Background(), p.ID, 11, 22))
	blocked := &blockingDeleteStore{faultStore: faultStore{Store: s.objects}, entered: make(chan struct{})}
	s.objects = blocked
	r := &Runtime{Service: s}
	r.Start()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup purge did not start")
	}
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime failed to cancel/join purge")
	}
	r.Close()
	r.Start()
	var row model.RelayMediaResource
	require.NoError(t, s.db.First(&row, "id = ?", p.ID).Error)
	assert.Equal(t, "revoked", row.State)
	s.objects = blocked.Store
	require.NoError(t, s.Purge(context.Background(), 100))
	var n int64
	require.NoError(t, s.db.Model(&model.RelayMediaResource{}).Count(&n).Error)
	assert.Zero(t, n)
}
