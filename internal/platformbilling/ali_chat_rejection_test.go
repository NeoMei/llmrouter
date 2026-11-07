package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAliMissingModelRejectionReleasesNativeHoldOnlyWithSupplierProof(t *testing.T) {
	const rejection = `{"error":{"message":"Model not exist.","type":"invalid_request_error","param":null,"code":"model_not_found"},"request_id":"supplier-request"}`
	for _, tc := range []struct {
		name, body, endpoint                          string
		status, channelType                           int
		insecure, reject, recoverOnly, releaseFailure bool
	}{
		{name: "definitive rejection", body: rejection, reject: true},
		{name: "crash before finish recovers", body: rejection, reject: true, recoverOnly: true},
		{name: "failed release recovers", body: rejection, reject: true, releaseFailure: true},
		{name: "other provider", body: rejection, channelType: constant.ChannelTypeOpenAI},
		{name: "other origin", body: rejection, endpoint: "https://proxy.example/compatible-mode/v1/chat/completions"},
		{name: "other endpoint", body: rejection, endpoint: "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation"},
		{name: "insecure TLS", body: rejection, insecure: true},
		{name: "server error", body: rejection, status: 500},
		{name: "contradictory usage", body: `{"error":{"code":"model_not_found","message":"Model not exist."},"usage":{"total_tokens":1}}`},
		{name: "output present", body: `{"error":{"code":"model_not_found","message":"Model not exist."},"choices":[]}`},
		{name: "unknown error", body: `{"error":{"code":"internal_error","message":"Model not exist."}}`},
		{name: "duplicate key", body: `{"error":{"code":"model_not_found","code":"internal_error","message":"Model not exist."}}`},
		{name: "truncated body", body: `{"error":{"code":"model_not_found"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _, _ := waiverFixture(t)

			var remote RemoteCall
			releases := 0
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/internal/v1/calls/call/release", r.URL.Path)
				var reason map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &reason))
				assert.True(t, strings.HasPrefix(reason["reason"], "ali_model_not_found:"))
				releases++
				if tc.releaseFailure && releases == 1 {
					w.WriteHeader(503)
					return
				}
				remote.Status, remote.Charged = "RELEASED", "0"
				b, e := common.Marshal(remote)
				require.NoError(t, e)
				_, _ = w.Write(b)
			}))
			defer hub.Close()
			a, e := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "native-service-token-at-least-32-bytes", InstanceID: "default", Group: "platform", NativeGroup: "default", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
			require.NoError(t, e)
			price := `{"native_price":"frozen"}`
			sum := sha256.Sum256([]byte(price))
			q := NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: hex.EncodeToString(sum[:]), BillingModel: "qwen-vl-max", WireOperation: "chat.completions.create", RouteID: "12", ChannelID: "101", Group: "default", NativeQuotaMax: "10", Conversion: NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "5", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}, Asset: "CNY/6", MaxAmount: "25"}
			qb, e := common.Marshal(q)
			require.NoError(t, e)
			row := Call{CallID: "call", CredentialID: "same-key", CredentialVersion: 2, ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "qwen-vl-max", Operation: "chat", WireOperation: "chat.completions.create", PricingSource: "llmrouter_native", NativePriceSnapshot: price, NativeQuoteJSON: string(qb), Digest: "request", Amount: "25", Asset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "UNKNOWN"}
			require.NoError(t, db.Create(&row).Error)
			attempt := Attempt{CallID: row.CallID, ChannelID: 101, Status: "DISPATCHED"}
			require.NoError(t, db.Create(&attempt).Error)
			s := &Session{adapter: a, call: row, attempt: attempt, uncertain: true}
			remote = RemoteCall{CredentialVersion: 2, TaskID: "task", CallID: "call", CredentialID: "same-key", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", InstanceID: "default", RequestDigest: "request", WireOperation: row.WireOperation, NativePricing: &q, Rate: Rate{Model: row.Model, Asset: "CNY/6", Semantics: "llmrouter_native"}, Held: "25", ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "RESERVED"}
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
			}
			status := tc.status
			if status == 0 {
				status = 404
			}
			kind := tc.channelType
			if kind == 0 {
				kind = constant.ChannelTypeAli
			}
			req, e := http.NewRequest(http.MethodPost, endpoint, nil)
			require.NoError(t, e)
			resp := &http.Response{StatusCode: status, Request: req, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			rejected, e := s.RejectAliMissingChatModel(context.Background(), kind, 101, resp, tc.insecure)
			require.NoError(t, e)
			assert.Equal(t, tc.reject, rejected)
			// Error processing must still receive the original supplier bytes.
			body, e := io.ReadAll(resp.Body)
			require.NoError(t, e)
			assert.Equal(t, tc.body, string(body))
			require.NoError(t, resp.Body.Close())
			if tc.recoverOnly {
				require.NoError(t, a.Recover(context.Background()))
			} else if tc.releaseFailure {
				require.Error(t, s.Finish(context.Background()))
				var pending Call
				require.NoError(t, db.First(&pending, "call_id = ?", row.CallID).Error)
				assert.Equal(t, "RELEASE_PENDING", pending.Status)
				assert.Contains(t, pending.Evidence, "supplier_rejection")
				require.NoError(t, a.Recover(context.Background()))
			} else {
				require.NoError(t, s.Finish(context.Background()))
			}
			var saved Call
			require.NoError(t, db.First(&saved, "call_id = ?", row.CallID).Error)
			var savedAttempt Attempt
			require.NoError(t, db.First(&savedAttempt, attempt.ID).Error)
			if tc.reject {
				assert.Equal(t, "RELEASED", saved.Status)
				assert.Equal(t, "0", saved.Charged)
				assert.Equal(t, "REJECTED", savedAttempt.Status)
				assert.Contains(t, saved.Evidence, "model_not_found")
				if tc.releaseFailure {
					assert.Equal(t, 2, releases)
				} else {
					assert.Equal(t, 1, releases)
				}
			} else {
				assert.Equal(t, "UNKNOWN", saved.Status)
				assert.Empty(t, saved.Charged)
				assert.Equal(t, "DISPATCHED", savedAttempt.Status)
				assert.Empty(t, saved.Evidence)
				assert.Zero(t, releases)
			}
			assert.False(t, s.CanRetry())
			assert.Equal(t, "superwagie", saved.OriginProductID)
			assert.Equal(t, "payer", saved.AccountID)
			assert.Equal(t, "same-key", saved.CredentialID)
		})
	}
}
