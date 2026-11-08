package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This boundary test catches credit purchase guidance emitted for budget/cash
// refusals or uncertain holds, and lost guidance for a proven CREDIT refusal.
func TestNativeCreditRefusalPublicContract(t *testing.T) {
	old := billing_setting.GetModelNativePricingCopy()
	t.Cleanup(func() { require.NoError(t, billing_setting.SetModelNativePricing(old)) })
	require.NoError(t, billing_setting.SetModelNativePricing(map[string]map[string]metering.Price{
		"refusal-model": {"embedding": {Version: "metered_v2", Operation: "embedding", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1000000"}}}}},
	}))
	for _, entry := range []string{"preconsume", "native-price", "ocr", "ocr-v2"} {
		for _, tc := range []struct {
			name, body, state string
			cash              bool
			status, wantHTTP  int
			creditRequired    bool
		}{
			{"credits", `{"error":"insufficient_credits"}`, "REJECTED", false, 402, 402, true},
			{"budget", `{"error":"usage_budget_exceeded"}`, "REJECTED", false, 402, 402, false},
			{"cash", `{"error":"insufficient_funds"}`, "REJECTED", true, 402, 402, false},
			{"cash-credit-body", `{"error":"insufficient_credits"}`, "REJECTED", true, 402, 402, false},
			{"credit-funds-body", `{"error":"insufficient_funds"}`, "REJECTED", false, 402, 402, false},
			{"ambiguous", `{"error":"insufficient_credits","hold":"unknown"}`, "RESERVE_UNKNOWN", false, 402, 503, false},
			{"duplicate", `{"error":"insufficient_credits","error":"usage_budget_exceeded"}`, "RESERVE_UNKNOWN", false, 402, 503, false},
			{"forbidden", `{"error":"insufficient_credits"}`, "RESERVE_UNKNOWN", false, 403, 403, false},
		} {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				conversion := platformbilling.NativeConversion{Version: "fixture-credit-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
				grant := platformbilling.Claims{ID: "key", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialKind: "api_key", CredentialVersion: 1, Asset: "CREDIT/6", FundingMode: "credits", ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource, NativeCreditConversion: &conversion}
				if tc.cash {
					grant.Asset, grant.FundingMode, grant.PricingSource, grant.NativeCreditConversion = "CNY/6", "prepaid_cash", "llmrouter_native", nil
				}
				hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/internal/v1/model-credentials/verify":
						data, err := common.Marshal(grant)
						require.NoError(t, err)
						_, _ = w.Write(data)
					case "/internal/v1/calls/native-reserve":
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					default:
						t.Errorf("unexpected service request %s", r.URL.Path)
						w.WriteHeader(500)
					}
				}))
				defer hub.Close()
				db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refusal.db"), &gorm.Config{})
				require.NoError(t, err)
				adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("s", 32), InstanceID: "default", Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
				require.NoError(t, err)
				session, err := adapter.AdmitNative(context.Background(), "uh_key_test", "task", "call", platformbilling.WireOperation{ID: "embeddings.create"}, "refusal-model", []byte(`{"model":"refusal-model"}`))
				require.NoError(t, err)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				path := "/v1/embeddings"
				if entry == "ocr-v2" {
					path = "/api/v2/ocr/jobs"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				common.SetContextKey(c, constant.ContextKeyChannelId, 1)
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
				info := &relaycommon.RelayInfo{PlatformMode: true, PlatformBilling: session, OriginModelName: "refusal-model", UsingGroup: "native", UserGroup: "native", RelayMode: relayconstant.RelayModeEmbeddings, Request: &dto.EmbeddingRequest{Model: "refusal-model", Input: "hello"}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}}
				info.SetEstimatePromptTokens(1)
				var apiErr *types.NewAPIError
				switch entry {
				case "preconsume":
					apiErr = service.PreConsumeBilling(c, 3, info)
				case "native-price":
					apiErr = prepareAttemptBilling(c, info, 1, &types.TokenCountMeta{})
				default:
					err = service.ReserveManagedNativePrice(c.Request.Context(), session, "refusal-model", "native", 0, 1, 3, []byte(`{"price":"fixture"}`))
					require.Error(t, err)
					(&OCRHandler{}).serviceError(c, err, 503)
				}
				wantHTTP := tc.wantHTTP
				if strings.HasPrefix(entry, "ocr") && !tc.creditRequired {
					// Preserve the existing fallback for all other OCR errors.
					wantHTTP = 503
				}
				if apiErr != nil {
					assert.True(t, types.IsSkipRetryError(apiErr))
					c.JSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
				}
				assert.Equal(t, wantHTTP, w.Code, w.Body.String())
				if tc.creditRequired {
					if entry == "ocr-v2" {
						assert.Contains(t, w.Body.String(), "platform_model_credits_required")
					} else {
						var body struct {
							Error struct {
								Code string `json:"code"`
							} `json:"error"`
						}
						require.NoError(t, common.Unmarshal(w.Body.Bytes(), &body))
						assert.Equal(t, "platform_model_credits_required", body.Error.Code)
					}
				} else {
					assert.NotContains(t, w.Body.String(), "platform_model_credits_required")
					if apiErr != nil {
						assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
					}
				}
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
				assert.Equal(t, tc.state, row.Status)
				var attempts int64
				require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
				assert.Zero(t, attempts)
			})
		}
	}
}
