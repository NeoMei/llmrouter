package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/ocr"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedNativeOCRZeroPriceSettlesBeforeOwnedDocument(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		name := "single-key"
		if pooled {
			name = "single-member-pool"
		}
		t.Run(name, func(t *testing.T) { testManagedNativeOCRLifecycle(t, pooled, false, false) })
	}
}
func TestManagedNativeOCROfficialBOSResultAndExistingJobRecovery(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-single-origin=%v", existing), func(t *testing.T) {
			testManagedNativeOCRLifecycle(t, true, true, existing)
		})
	}
}

func testManagedNativeOCRLifecycle(t *testing.T, pooled, officialBOS, existingSnapshot bool) {
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
	variant := "success"
	if officialBOS {
		variant = "native-bos"
	}
	f = newOCRGatewayFixture(t, variant, hub.URL, "service-service-service-service-token", "", "")
	oldPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"PaddleOCR-VL-1.6":0}`))
	cfg := f.cfg
	cfg.NativeGroup = "platform"
	a, e := platformbilling.New(f.db, cfg)
	require.NoError(t, e)
	r := gin.New()
	SetManagedKeyDispatch(r, a)
	if pooled {
		info := model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled}}
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(map[string]any{"channel_info": info, "key": "synthetic-paddle-key\n"}).Error)
		ct, body := ocrMultipart(t, f.image)
		reject := func(id string) {
			req := httptest.NewRequest(http.MethodPost, "/platform/v1/ocr/jobs", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer uh_key_ocr")
			req.Header.Set("Content-Type", ct)
			req.Header.Set("Idempotency-Key", id)
			req.Header.Set("X-UniHub-Task-ID", "ocr-task")
			w := httptest.NewRecorder()
			newManagedNativeRouter(a).ServeHTTP(w, req)
			require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			assert.Zero(t, f.sends)
			assert.Zero(t, f.reserves)
			var call platformbilling.Call
			require.NoError(t, f.db.Where("call_id = ?", id).First(&call).Error, "negative case must pass credential admission")
			assert.Equal(t, "REJECTED", call.Status)
		}
		disabled := info
		disabled.MultiKeyStatusList = map[int]int{0: common.ChannelStatusAutoDisabled}
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("channel_info", disabled).Error)
		reject("disabled-pool")
		multiple := info
		multiple.MultiKeySize = 2
		multiple.MultiKeyStatusList = map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusAutoDisabled}
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(map[string]any{"channel_info": multiple, "key": "synthetic-paddle-key\nsecond-key"}).Error)
		reject("multiple-members")
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(map[string]any{"channel_info": info, "key": "synthetic-paddle-key\n"}).Error)
		var channel model.Channel
		require.NoError(t, f.db.First(&channel, 1).Error)
		refs, _, err := model.ChannelAccountRefs(context.Background(), f.db, &channel)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		scope := model.UpstreamAccountScope{Operation: ocr.OCROperation, Model: "PaddleOCR-VL-1.6"}
		selected, err := model.SelectUpstreamAccount(context.Background(), f.db, refs, scope, nil, time.Now())
		require.NoError(t, err)
		require.NoError(t, model.RecordUpstreamAccountFailure(context.Background(), f.db, selected, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(time.Hour)}, time.Now()))
		reject("cooled-member")
		require.NoError(t, f.db.Where("1 = 1").Delete(&model.UpstreamAccountState{}).Error)
	}
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
	if pooled {
		var job model.OCRJob
		require.NoError(t, f.db.Where("platform_call_id = ?", call).First(&job).Error)
		var binding model.OCRAccountBinding
		require.NoError(t, common.UnmarshalJsonStr(job.AccountBinding, &binding))
		assert.Equal(t, 1, binding.Ref.ChannelID)
		assert.Equal(t, 0, binding.Ref.KeyIndex)
		assert.NotEmpty(t, binding.Ref.MemberID)
		var execution model.OCRExecution
		require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
		expected := sha256.Sum256([]byte("synthetic-paddle-key"))
		assert.Equal(t, hex.EncodeToString(expected[:]), execution.KeySHA256)
		for _, change := range []map[string]any{
			{"channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusAutoDisabled}}},
			{"channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled}}, "key": "rotated-key"},
		} {
			require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(change).Error)
			denied := httptest.NewRequest(http.MethodGet, "/v1/ocr/"+call, nil)
			denied.Header.Set("Authorization", "Bearer uh_key_ocr")
			out := httptest.NewRecorder()
			r.ServeHTTP(out, denied)
			assert.GreaterOrEqual(t, out.Code, 400, out.Body.String())
			assert.Zero(t, f.polls)
			assert.Zero(t, f.settles)
		}
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(map[string]any{"channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled}}, "key": "synthetic-paddle-key\n"}).Error)
	}
	var preservedExecution string
	if officialBOS {
		var job model.OCRJob
		require.NoError(t, f.db.Where("platform_call_id = ?", call).First(&job).Error)
		var execution model.OCRExecution
		require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
		if existingSnapshot {
			// Reproduce the already accepted rc21 job; recovery must not rewrite it.
			execution.ResourceOrigins = []string{"https://paddleocr.aistudio-app.com"}
			encoded, err := common.Marshal(execution)
			require.NoError(t, err)
			require.NoError(t, f.db.Model(&job).Updates(map[string]any{"execution": string(encoded), "provider_state": "done", "total_pages": 1, "extracted_pages": 1}).Error)
			f.polls = 1 // The original supplier has already completed the accepted job.
			var originalCall platformbilling.Call
			require.NoError(t, f.db.First(&originalCall, "call_id = ?", call).Error)
			for _, change := range []map[string]any{
				{"pricing_source": ""},
				{"status": "RESERVED"},
				{"provider_task_id": "different-provider-job"},
				{"credential_kind": "task"},
			} {
				require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", call).Updates(change).Error)
				_, err := f.runtime.Service.Status(context.Background(), job.ID, job.UserID, job.TokenID)
				require.Error(t, err)
				assert.Zero(t, f.downloads)
				assert.Zero(t, f.settles)
				require.NoError(t, f.db.Save(&originalCall).Error)
			}
			custom := execution
			custom.JobsURL = "https://custom-paddle.example/api/v2/ocr/jobs"
			customRaw, err := common.Marshal(custom)
			require.NoError(t, err)
			require.NoError(t, f.db.Model(&job).Update("execution", string(customRaw)).Error)
			_, err = f.runtime.Service.Status(context.Background(), job.ID, job.UserID, job.TokenID)
			require.Error(t, err)
			assert.Zero(t, f.downloads)
			require.NoError(t, f.db.Model(&job).Update("execution", string(encoded)).Error)
		} else {
			assert.Contains(t, execution.ResourceOrigins, "https://paddleocr-store.bj.bcebos.com")
		}
		require.NoError(t, f.db.First(&job, "id = ?", job.ID).Error)
		preservedExecution = job.Execution
		for _, deniedOrigin := range []string{
			"https://paddleocr-store.bj.bcebos.com.attacker.example",
			"https://other-bucket.bj.bcebos.com",
			"http://paddleocr-store.bj.bcebos.com",
			"https://paddleocr-store.bj.bcebos.com:8443",
			"https://user@paddleocr-store.bj.bcebos.com",
			"https://paddleocr-store.bj.bcebos.com#fragment",
			"http://127.0.0.1",
		} {
			f.polls = max(f.polls, 1)
			f.resultOrigin = deniedOrigin
			_, err := f.runtime.Service.Status(context.Background(), job.ID, job.UserID, job.TokenID)
			require.Error(t, err, deniedOrigin)
			assert.Zero(t, f.downloads)
			assert.Zero(t, f.settles)
		}
		f.resultOrigin = "https://paddleocr-store.bj.bcebos.com"
	}
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
	if officialBOS {
		var job model.OCRJob
		require.NoError(t, f.db.Where("platform_call_id = ?", call).First(&job).Error)
		assert.Equal(t, preservedExecution, job.Execution)
		assert.Equal(t, "settled", job.BillingState)
		assert.Equal(t, 1, job.SubmitAttempts)
		assert.NotEmpty(t, job.ResultResourceID)
	}
}
