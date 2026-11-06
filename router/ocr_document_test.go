package router

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagedOCRDocumentCurrentKeyAndResourceBoundary(t *testing.T) {
	f := newOCRGatewayFixture(t, "success", "", "", "", "")
	f.originProduct = "superwagie"
	ct, body := ocrMultipart(t, f.image)
	require.Equal(t, 200, f.request(t, "POST", "/platform/api/v2/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body).Code)
	for i := 0; i < 2; i++ {
		out := f.request(t, "GET", "/platform/v1/ocr/jobs/ocr-call", "uh_read_"+strings.Repeat("b", 64), "", nil)
		require.Equal(t, 200, out.Code, out.Body.String())
	}
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "ocr-call").Update("credential_kind", "api_key").Error)
	revoked, verifies, revokeAfter := false, 0, 0
	actor, account, instance, credentialID, scopedModel := "actor", "account", "default", "original-credential", "ocr-model"
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/model-credentials/verify", r.URL.Path)
		verifies++
		if revoked || (revokeAfter > 0 && verifies >= revokeAfter) {
			w.WriteHeader(403)
			return
		}
		claims := platformbilling.Claims{CredentialKind: "api_key", ID: credentialID, Audience: "llmrouter", ActorID: actor, AccountID: account, OriginProductID: "superwagie", InstanceID: instance, Models: []string{scopedModel}, Operations: map[string][]string{"ocr-model": {"ocr.jobs.create"}}, ExpiresAt: time.Now().Add(time.Hour), Asset: "CNY/6", FundingMode: "prepaid_cash"}
		raw, err := common.Marshal(claims)
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	defer hub.Close()
	cfg := f.cfg
	cfg.Origin = hub.URL
	adapter, err := platformbilling.New(f.db, cfg)
	require.NoError(t, err)
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	service.PlatformTaskSettlementCheck = f.adapter.VerifyTaskSettlement
	read := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		engine.ServeHTTP(out, r)
		return out
	}
	token := "uh_key_fixture_document"
	polls, downloads := f.polls, f.downloads
	out := read("/v1/ocr/ocr-call/document", token)
	require.Equal(t, 200, out.Code, out.Body.String())
	assert.Contains(t, out.Body.String(), "recognized")
	for _, unsafe := range []string{"images", "paddle.example", "token=", "raw", "native"} {
		assert.NotContains(t, out.Body.String(), unsafe)
	}
	assert.Equal(t, "no-store", out.Header().Get("Cache-Control"))
	assert.Equal(t, polls, f.polls)
	assert.Equal(t, downloads, f.downloads)
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 404, read("/v1/ocr/ocr-call/result", token).Code)
	for _, badToken := range []string{"uh_model_" + strings.Repeat("a", 64), "uh_read_" + strings.Repeat("b", 64)} {
		assert.NotEqual(t, 200, read("/platform/v1/ocr/jobs/ocr-call/document", badToken).Code)
	}
	assert.Equal(t, 400, read("/v1/ocr/ocr-call/document?token=unused", token).Code)
	for _, field := range []*string{&actor, &account, &instance, &credentialID, &scopedModel} {
		original := *field
		*field = "foreign"
		assert.Equal(t, 404, read("/v1/ocr/ocr-call/document", token).Code)
		*field = original
	}
	revoked = true
	assert.Equal(t, 404, read("/v1/ocr/ocr-call/document", token).Code)
	revoked = false
	verifies = 0
	revokeAfter = 3
	assert.Equal(t, 404, read("/v1/ocr/ocr-call/document", token).Code)
	revokeAfter = 0
	f.denySettlementRead = true
	assert.NotEqual(t, 200, read("/v1/ocr/ocr-call/document", token).Code)
	f.denySettlementRead = false

	var job model.OCRJob
	require.NoError(t, f.db.First(&job, "platform_call_id = ?", "ocr-call").Error)
	var resource model.OCRResource
	require.NoError(t, f.db.First(&resource, "id = ?", job.ResultResourceID).Error)
	for _, change := range []map[string]any{{"revoked": true}, {"expires_at": f.now.Unix() - 1}, {"checksum": strings.Repeat("0", 64)}} {
		require.NoError(t, f.db.Model(&model.OCRResource{}).Where("id = ?", resource.ID).Updates(change).Error)
		assert.Equal(t, 409, read("/v1/ocr/ocr-call/document", token).Code)
		require.NoError(t, f.db.Save(&resource).Error)
	}
	svcCfg := f.serviceCfg
	svcCfg.Objects = revokeDocumentObjectStore{Store: f.serviceCfg.Objects, afterOpen: func() {
		require.NoError(t, f.db.Model(&model.OCRResource{}).Where("id = ?", resource.ID).Update("revoked", true).Error)
	}}
	f.runtime.Service, err = ocr.NewService(f.db, f.runtime.Billing, svcCfg)
	require.NoError(t, err)
	assert.Equal(t, 409, read("/v1/ocr/ocr-call/document", token).Code)
	assert.Equal(t, 1, f.sends)
}

type revokeDocumentObjectStore struct {
	objectstore.Store
	afterOpen func()
}

func (s revokeDocumentObjectStore) Open(ctx context.Context, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	body, info, err := s.Store.Open(ctx, key)
	if err == nil {
		s.afterOpen()
	}
	return body, info, err
}
