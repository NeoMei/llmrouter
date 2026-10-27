package router

import (
	"bytes"
	"context"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestRelayMediaHTTPImmutableRead(t *testing.T) {
	enableNativeCompatibility(t)
	setting := system_setting.GetFetchSetting()
	old := *setting
	*setting = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
	defer func() { *setting = old }()
	service.InitHttpClient()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	require.NoError(t, db.AutoMigrate(&model.RelayMediaResource{}))
	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	s, err := relaymedia.NewService(db, relaymedia.Config{PublicOrigin: "https://snapshots.example", GrantSecret: bytes.Repeat([]byte{8}, 32), Objects: objects})
	require.NoError(t, err)
	data, err := os.ReadFile("../service/relaymedia/testdata/h264.mp4")
	require.NoError(t, err)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer source.Close()
	p, err := s.PrepareMP4(context.Background(), relaymedia.PrepareInput{UserID: 11, TokenID: 22, RequestID: uuid.NewString(), SourceURL: source.URL})
	require.NoError(t, err)
	u, err := url.Parse(p.URL)
	require.NoError(t, err)
	engine := gin.New()
	SetRelayMediaRouter(engine, &controller.RelayMediaHandler{Service: s})
	for _, tc := range []struct {
		name, method, rng string
		status            int
		want              []byte
	}{{"full", http.MethodGet, "", 200, data}, {"repeated", http.MethodGet, "", 200, data}, {"head", http.MethodHead, "", 200, nil}, {"first", http.MethodGet, "bytes=0-31", 206, data[:32]}, {"suffix", http.MethodGet, "bytes=-8", 206, data[len(data)-8:]}, {"invalid", http.MethodGet, "bytes=90000000-", 416, nil}, {"multi-range", http.MethodGet, "bytes=0-1,3-4", 416, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, u.RequestURI(), nil)
			req.Header.Set("Range", tc.rng)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			assert.Equal(t, tc.status, w.Code)
			if tc.status != 416 {
				assert.Equal(t, tc.want, w.Body.Bytes())
				assert.Equal(t, "video/mp4", w.Header().Get("Content-Type"))
			}
			assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
			assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
			assert.Empty(t, w.Header().Get("Set-Cookie"))
		})
	}
	for _, path := range []string{u.Path, u.RequestURI() + "tampered", "/api/relay-media/resources/" + uuid.NewString() + "?token=" + u.Query().Get("token")} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assert.Equal(t, 404, w.Code)
		assert.Empty(t, w.Body.Bytes())
	}
	require.NoError(t, s.Revoke(context.Background(), p.ID, 11, 22))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", u.RequestURI(), nil))
	assert.Equal(t, 404, w.Code)
	assert.Empty(t, w.Body.Bytes())
}
