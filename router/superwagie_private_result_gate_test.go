package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A receipt credential proves settlement scope, not current login or membership.
// Until result-read authorization and revocation are serialized, it must never
// release a SuperWagie private artifact.
func TestSuperWagiePrivateMediaReadRequiresStrongerAuthority(t *testing.T) {
	modelToken := "uh_model_" + strings.Repeat("a", 64)
	ownedToken := "uh_read_" + strings.Repeat("b", 64)
	t.Run("video result", func(t *testing.T) {
		f := newWanVideoGatewayFixture(t, "720")
		f.originProduct = "superwagie"
		require.Equal(t, http.StatusOK, f.submit(t, "720P").Code)
		cancelReq := httptest.NewRequest(http.MethodPost, "/platform/v1/videos/wan-call/cancel", nil)
		cancelReq.Header.Set("Authorization", "Bearer "+modelToken)
		cancelReq.Header.Set("X-UniHub-Task-ID", "wan-task")
		cancel := httptest.NewRecorder()
		f.engine.ServeHTTP(cancel, cancelReq)
		assert.Equal(t, http.StatusNotFound, cancel.Code)
		assert.Zero(t, f.cancels)
		oldLimit := constant.TaskQueryLimit
		constant.TaskQueryLimit = 10
		t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
		_ = service.RunTaskPollingOnce(context.Background(), nil)
		require.Equal(t, 1, f.downloads)
		for _, token := range []string{modelToken, ownedToken} {
			result := f.read(t, "/result", token)
			assert.Equal(t, http.StatusNotFound, result.Code)
			assert.NotEqual(t, len(f.mp4), result.Body.Len())
		}
		assert.Equal(t, 1, f.sends)
		status := f.read(t, "", modelToken)
		assert.Equal(t, http.StatusOK, status.Code)
		r := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/wan-call", nil)
		r.Header.Set("Authorization", "Bearer "+modelToken)
		r.Header.Set("X-UniHub-Task-ID", "wan-task")
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"status":"SETTLED"`)
	})
	t.Run("masked image", func(t *testing.T) {
		f := newImageGatewayFixture(t, "wan-edit", "", "", "")
		f.originProduct = "superwagie"
		path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
		require.Equal(t, http.StatusAccepted, f.request(t, path, contentType, body).Code)
		req := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
		req.Header.Set("Authorization", "Bearer "+modelToken)
		req.Header.Set("X-UniHub-Task-ID", "image-task")
		out := httptest.NewRecorder()
		f.engine.ServeHTTP(out, req)
		assert.Equal(t, http.StatusNotFound, out.Code)
		assert.NotContains(t, out.Body.String(), "b64_json")
		assert.Zero(t, f.downloads.Load())
		// A service-owned recovery may complete the original task, but it must
		// not make its private output readable with a receipt-only credential.
		require.NoError(t, recoverWanImage(context.Background(), f.adapter, "image-call"))
		assert.EqualValues(t, 1, f.downloads.Load())
		settled := httptest.NewRecorder()
		f.engine.ServeHTTP(settled, req.Clone(req.Context()))
		assert.Equal(t, http.StatusNotFound, settled.Code)
		assert.NotContains(t, settled.Body.String(), "b64_json")
		assert.EqualValues(t, 1, f.sends.Load())
		// Exercise the SDK alias through the actual managed-key rewrite and the
		// same frozen channel/attempt binding used by the platform route.
		require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "image-call").Update("credential_kind", "api_key").Error)
		_, err := f.adapter.ManagedTask(context.Background(), "uh_key_image_fixture", "image-call")
		require.NoError(t, err)
		engine := gin.New()
		SetManagedKeyDispatch(engine, f.adapter)
		for _, alias := range []string{"/v1/images/edits/image-call", "/platform/v1/images/edits/image-call"} {
			aliasReq := httptest.NewRequest(http.MethodGet, alias, nil)
			aliasReq.Header.Set("Authorization", "Bearer uh_key_image_fixture")
			aliasResult := httptest.NewRecorder()
			engine.ServeHTTP(aliasResult, aliasReq)
			assert.Equal(t, http.StatusNotFound, aliasResult.Code, alias)
			assert.NotContains(t, aliasResult.Body.String(), "b64_json")
		}
		assert.EqualValues(t, 1, f.downloads.Load())
	})
	t.Run("other product masked image SDK alias remains usable", func(t *testing.T) {
		f := newImageGatewayFixture(t, "wan-edit", "", "", "")
		path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
		require.Equal(t, http.StatusAccepted, f.request(t, path, contentType, body).Code)
		require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "image-call").Update("credential_kind", "api_key").Error)
		_, err := f.adapter.ManagedTask(context.Background(), "uh_key_image_fixture", "image-call")
		require.NoError(t, err)
		engine := gin.New()
		SetManagedKeyDispatch(engine, f.adapter)
		r := httptest.NewRequest(http.MethodGet, "/v1/images/edits/image-call", nil)
		r.Header.Set("Authorization", "Bearer uh_key_image_fixture")
		out := httptest.NewRecorder()
		engine.ServeHTTP(out, r)
		require.Equal(t, http.StatusOK, out.Code, out.Body.String())
		assert.Contains(t, out.Body.String(), "b64_json")
		assert.EqualValues(t, 1, f.sends.Load())
	})
	t.Run("native OCR returns artifact URLs", func(t *testing.T) {
		f := newOCRGatewayFixture(t, "success", "", "", "", "")
		f.originProduct = "superwagie"
		ct, body := ocrMultipart(t, f.image)
		require.Equal(t, http.StatusOK, f.request(t, http.MethodPost, "/platform/api/v2/ocr/jobs", modelToken, ct, body).Code)
		out := f.request(t, http.MethodGet, "/platform/api/v2/ocr/jobs/ocr-call", ownedToken, "", nil)
		assert.Equal(t, http.StatusNotFound, out.Code)
		assert.False(t, bytes.Contains(out.Body.Bytes(), []byte("resultUrl")))
		standardResult := f.request(t, http.MethodGet, "/platform/v1/ocr/jobs/ocr-call/result", ownedToken, "", nil)
		assert.Equal(t, http.StatusNotFound, standardResult.Code)
		assert.NotContains(t, standardResult.Body.String(), "recognized")
		standardStatus := f.request(t, http.MethodGet, "/platform/v1/ocr/jobs/ocr-call", ownedToken, "", nil)
		assert.Equal(t, http.StatusOK, standardStatus.Code)
		assert.Equal(t, 1, f.sends)
	})
	t.Run("previously issued OCR resource grant", func(t *testing.T) {
		f := newOCRGatewayFixture(t, "success", "", "", "", "")
		f.originProduct = "superwagie"
		ct, body := ocrMultipart(t, f.image)
		require.Equal(t, http.StatusOK, f.request(t, http.MethodPost, "/platform/api/v2/ocr/jobs", modelToken, ct, body).Code)
		var row platformbilling.Call
		require.NoError(t, f.db.First(&row, "call_id = ?", "ocr-call").Error)
		// Produce a grant the old handler could have issued before the gate.
		var out *httptest.ResponseRecorder
		for i := 0; i < 2; i++ {
			out = httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodGet, "/platform/api/v2/ocr/jobs/ocr-call", nil)
			c.Set("platform_task_call", row)
			c.Set("platform_ocr_native", true)
			controller.PlatformTaskStatus(c)
			require.Equal(t, http.StatusOK, out.Code, out.Body.String())
		}
		var status struct {
			Data ocr.JobStatus `json:"data"`
		}
		require.NoError(t, common.Unmarshal(out.Body.Bytes(), &status))
		grantURL, err := url.Parse(status.Data.ResultURL.JSONURL)
		require.NoError(t, err)
		require.NotEmpty(t, grantURL.RawQuery)
		result := f.request(t, http.MethodGet, grantURL.RequestURI(), "", "", nil)
		assert.Equal(t, http.StatusNotFound, result.Code)
		assert.NotContains(t, result.Body.String(), "recognized")
	})
}
