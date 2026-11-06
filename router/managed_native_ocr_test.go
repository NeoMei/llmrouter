package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedNativeOCRZeroPriceSettlesBeforeOwnedDocument(t *testing.T) {
	var remote platformbilling.RemoteCall
	var final platformbilling.NativeSettleRequest
	var f *ocrGatewayFixture
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			out = platformbilling.Claims{ID: "ocr-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", CredentialVersion: 2, Asset: "CNY/6", FundingMode: "prepaid_cash"}
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			f.reserves++
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "0", in.Quote.MaxAmount)
			remote = platformbilling.RemoteCall{CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: "ocr-key", InstanceID: "default", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote, Rate: platformbilling.Rate{Model: in.Model, Asset: "CNY/6", Semantics: "llmrouter_native"}, Held: "0", ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "RESERVED"}
			out = remote
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			f.settles++
			require.NoError(t, common.DecodeJson(r.Body, &final))
			assert.Equal(t, "0", final.NativeQuota)
			assert.Equal(t, "0", final.Amount)
			remote.Status, remote.Charged = "SETTLED", "0"
			out = remote
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/v1/calls/"):
			out = struct {
				platformbilling.RemoteCall
				NativeQuota string                 `json:"native_quota"`
				Usage       *platformbilling.Usage `json:"usage"`
			}{remote, final.NativeQuota, final.Usage}
		default:
			t.Errorf("unexpected native OCR path %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		b, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	defer hub.Close()
	f = newOCRGatewayFixture(t, "success", hub.URL, "service-service-service-service-token", "", "")
	oldPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"PaddleOCR-VL-1.6":0}`))
	cfg := f.cfg
	cfg.NativeGroup = "platform"
	a, e := platformbilling.New(f.db, cfg)
	require.NoError(t, e)
	r := gin.New()
	SetManagedKeyDispatch(r, a)
	ct, body := ocrMultipart(t, f.image)
	req := httptest.NewRequest(http.MethodPost, "/platform/v1/ocr/jobs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer uh_key_ocr")
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Idempotency-Key", "ocr-call")
	req.Header.Set("X-UniHub-Task-ID", "ocr-task")
	w := httptest.NewRecorder()
	newManagedNativeRouter(a).ServeHTTP(w, req)
	require.Equal(t, 202, w.Code, w.Body.String())
	call := w.Header().Get("X-UniHub-Call-ID")
	require.NotEmpty(t, call)
	for range 2 {
		req = httptest.NewRequest(http.MethodGet, "/v1/ocr/"+call, nil)
		req.Header.Set("Authorization", "Bearer uh_key_ocr")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/ocr/"+call+"/document", nil)
	req.Header.Set("Authorization", "Bearer uh_key_ocr")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "recognized")
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 1, f.reserves)
	assert.Equal(t, 1, f.settles)
}
