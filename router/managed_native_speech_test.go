package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type managedSpeechFixture struct {
	engine                              *gin.Engine
	db                                  *gorm.DB
	adapter                             *platformbilling.Adapter
	mode                                string
	sends, downloads, reserves, settles int
	verifies                            int
	text, reserveQuota                  string
	supplierCharacters                  int
	settlement                          platformbilling.NativeSettleRequest
}

func newManagedSpeechFixture(t *testing.T) *managedSpeechFixture {
	t.Helper()
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	_, db := providerCatalogFixture(t)
	f := &managedSpeechFixture{db: db, text: "你好 A", reserveQuota: "6000", supplierCharacters: 3}
	oldPrices, oldLimits := billing_setting.GetModelNativePricingCopy(), billing_setting.GetModelNativeReservationLimitsCopy()
	oldGroups, oldQuota := ratio_setting.GroupRatio2JSONString(), common.QuotaPerUnit
	oldProducer, oldCache, oldRedis, oldGlobal := newNativeSpeechProducer, common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable
	t.Cleanup(func() {
		require.NoError(t, billing_setting.SetModelNativePricing(oldPrices))
		require.NoError(t, billing_setting.SetModelNativeReservationLimits(oldLimits))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
		common.QuotaPerUnit = oldQuota
		newNativeSpeechProducer, common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable = oldProducer, oldCache, oldRedis, oldGlobal
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable = false, false, false
	common.QuotaPerUnit = 1000000
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"native":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"voice":1}`))
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "100000"}}}}
	require.NoError(t, billing_setting.SetModelNativePricing(map[string]map[string]metering.Price{"voice": {"speech": price}}))
	require.NoError(t, billing_setting.SetModelNativeReservationLimits(map[string]map[string]metering.Quantity{"voice": {"speech": {Value: "600"}}}))
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "native", Quota: 777, UsedQuota: 8, RequestCount: 9}).Error)
	origin := "https://dashscope.aliyuncs.com"
	require.NoError(t, db.Create(&model.Channel{Id: 106, Type: constant.ChannelTypeAli, Key: "supplier-speech-secret", Status: common.ChannelStatusEnabled, BaseURL: &origin, Group: "native", Models: "qwen3-tts-instruct-flash"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "qwen3-tts-instruct-flash", ChannelId: 106, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "qwen3-tts-flash", ChannelId: 106, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "voice", Client: "all", Enabled: true, Metadata: `{"operations":["bailian.audio.speech.create"],"input_modalities":["text"],"output_modalities":["audio"]}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 106, UpstreamModel: "qwen3-tts-instruct-flash", BillingModel: "voice", PreferredWires: `[]`, Enabled: true, Weight: 1}).Error)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			f.downloads++
			assert.Empty(t, r.Header.Get("Authorization"))
			if f.mode == "download-failure" {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(audioWAV())
			return
		}
		f.sends++
		assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
		assert.Equal(t, "Bearer supplier-speech-secret", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("X-UniHub-Task-ID"))
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		expected, err := common.Marshal(gin.H{"model": "qwen3-tts-instruct-flash", "input": gin.H{"text": f.text, "voice": "Cherry", "instructions": "Read calmly", "optimize_instructions": false}})
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(raw))
		w.Header().Set("Content-Type", "application/json")
		usage := fmt.Sprintf(`,"usage":{"characters":%d}`, f.supplierCharacters)
		if f.mode == "missing-usage" {
			usage = ""
		}
		if f.mode == "zero" {
			usage = `,"usage":{"characters":0}`
		}
		_, _ = fmt.Fprintf(w, `{"request_id":"speech-request","output":{"finish_reason":"stop","audio":{"url":"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/tone.wav?signature=private"}}%s}`, usage)
	}))
	t.Cleanup(provider.Close)
	newNativeSpeechProducer = func(target nativespeech.Target) (*nativespeech.Producer, error) {
		return nativespeech.NewWithTransport(target, audioFixtureTransport{origin: provider.URL, transport: provider.Client().Transport})
	}
	var remote platformbilling.RemoteCall
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var output any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			f.verifies++
			if f.verifies == 3 {
				switch f.mode {
				case "changed-target-final-verify":
					require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 1).Update("upstream_model", "qwen3-tts-flash").Error)
				case "changed-target-id-final-verify":
					require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 1).Update("id", 2).Error)
				case "changed-key-final-verify":
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 106).Update("key", "rotated-supplier-key").Error)
				}
			}
			output = platformbilling.Claims{ID: "original-key", CredentialKind: "api_key", InstanceID: "fixture", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", CredentialVersion: 2, Asset: "CNY/6", FundingMode: "prepaid_cash"}
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			f.reserves++
			if f.mode == "budget" {
				w.WriteHeader(402)
				_, _ = w.Write([]byte(`{"error":"insufficient_funds"}`))
				return
			}
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "uh_key_same", in.Credential)
			assert.Equal(t, "bailian.audio.speech.create", in.WireOperation)
			assert.Equal(t, f.reserveQuota, in.Quote.NativeQuotaMax)
			remote = platformbilling.RemoteCall{CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: "original-key", InstanceID: "fixture", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote, Rate: platformbilling.Rate{Model: in.Model, Asset: "CNY/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "RESERVED"}
			if f.mode == "revoked-route" {
				require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("enabled", false).Error)
			}
			if f.mode == "changed-target" {
				require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 1).Update("upstream_model", "qwen3-tts-flash").Error)
			}
			if f.mode == "changed-price" {
				changed := price
				changed.Terms = []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "100"}}}
				require.NoError(t, billing_setting.SetModelNativePricing(map[string]map[string]metering.Price{"voice": {"speech": changed}}))
			}
			output = remote
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			f.settles++
			require.NoError(t, common.DecodeJson(r.Body, &f.settlement))
			if f.mode == "settlement-unavailable" {
				w.WriteHeader(503)
				return
			}
			remote.Status, remote.Charged = "SETTLED", f.settlement.Amount
			output = remote
		case strings.HasSuffix(r.URL.Path, "/release"):
			remote.Status = "RELEASED"
			output = remote
		default:
			t.Errorf("unexpected funding endpoint %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		raw, err := common.Marshal(output)
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	var err error
	f.adapter, err = platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("s", 32), InstanceID: "fixture", PrincipalID: 1, Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, f.adapter)
	SetRelayRouter(f.engine)
	return f
}

func TestManagedBailianSpeechReservesChineseBillingUnitsBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name, limit, reserve string
		characters, status   int
	}{
		{"full Chinese input", "1200", "12000", 600, 200},
		{"insufficient configured bound", "600", "6000", 301, 400},
		{"input limit remains 600 scalars", "1200", "12000", 601, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			f.text, f.reserveQuota, f.supplierCharacters = strings.Repeat("你", tc.characters), tc.reserve, 2*tc.characters
			require.NoError(t, billing_setting.SetModelNativeReservationLimits(map[string]map[string]metering.Quantity{"voice": {"speech": {Value: tc.limit}}}))
			body := strings.Replace(managedSpeechBody, "你好 A", f.text, 1)
			out := f.request(body, "speech-chinese")
			require.Equal(t, tc.status, out.Code)
			if tc.status != 200 {
				assert.Zero(t, f.reserves)
				assert.Zero(t, f.sends)
				return
			}
			assert.Equal(t, "12000", f.settlement.NativeQuota)
			require.NotNil(t, f.settlement.Usage)
			require.NotNil(t, f.settlement.Usage.Metered)
			assert.Equal(t, "1200", f.settlement.Usage.Metered.Measurements[0].Quantity.Value)
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			assert.Equal(t, audioWAV(), out.Body.Bytes())
		})
	}
}

func TestManagedBailianSpeechFinalCredentialVerificationCannotChangeTarget(t *testing.T) {
	for _, mode := range []string{"changed-target-final-verify", "changed-target-id-final-verify", "changed-key-final-verify"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			f.mode = mode
			out := f.request(managedSpeechBody, "speech-final-check")
			assert.Equal(t, 409, out.Code)
			assert.Equal(t, 3, f.verifies, "mutation occurs during final remote credential verification")
			assert.Zero(t, f.sends)
			assert.Zero(t, f.settles)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call).Error)
			assert.Equal(t, "RELEASED", call.Status)
		})
	}
}

func (f *managedSpeechFixture) request(body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/native/bailian/audio/speech", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer uh_key_same")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	out := httptest.NewRecorder()
	f.engine.ServeHTTP(out, req)
	return out
}

const managedSpeechBody = `{"model":"voice","input":{"text":"你好 A","voice":"Cherry","instructions":"Read calmly","optimize_instructions":false}}`

func TestManagedBailianSpeechSameKeyCatalogProtocolAndSupplierSettlement(t *testing.T) {
	f := newManagedSpeechFixture(t)
	catalog, err := readProviderCatalog(context.Background(), f.adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
	require.NoError(t, err)
	require.Len(t, catalog.Models, 1)
	require.True(t, catalog.Models[0].Probe)
	op := catalog.Models[0].CapabilityMetadata.Operations[0]
	assert.Equal(t, "bailian", op.Protocol)
	assert.Equal(t, "/v1/native/bailian/audio/speech", op.Endpoint)
	assert.Equal(t, []string{"binary"}, op.Transports)
	out := f.request(managedSpeechBody, "speech-success")
	require.Equal(t, 200, out.Code, out.Body.String())
	assert.Equal(t, "audio/wav", out.Header().Get("Content-Type"))
	assert.Equal(t, audioWAV(), out.Body.Bytes())
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 1, f.downloads)
	assert.Equal(t, 1, f.reserves)
	assert.Equal(t, 1, f.settles)
	assert.Equal(t, "30", f.settlement.NativeQuota, "supplier reports three characters; request text contains four scalars")
	require.NotNil(t, f.settlement.Usage)
	require.NotNil(t, f.settlement.Usage.Metered)
	assert.Equal(t, "3", f.settlement.Usage.Metered.Measurements[0].Quantity.Value)
	var call platformbilling.Call
	require.NoError(t, f.db.First(&call, "call_id = ?", out.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "SETTLED", call.Status)
	assert.Contains(t, call.AudioObservation, "speech-request")
	assert.NotContains(t, call.AudioObservation+call.NativePriceSnapshot, "supplier-speech-secret")
	assert.NotContains(t, call.AudioObservation, "signature=private")
	var user model.User
	require.NoError(t, f.db.First(&user, 1).Error)
	assert.Equal(t, 777, user.Quota)
	assert.Equal(t, 8, user.UsedQuota)
	assert.Equal(t, 9, user.RequestCount)
	duplicate := f.request(managedSpeechBody, "speech-success")
	assert.Equal(t, 409, duplicate.Code)
	assert.Equal(t, 1, f.sends)
}

func TestManagedBailianSpeechFailureDoesNotReturnAudioOrInventUsage(t *testing.T) {
	for _, tc := range []struct {
		mode, status             string
		httpCode, sends, settles int
	}{
		{"missing-usage", "UNKNOWN", 502, 1, 0}, {"download-failure", "UNKNOWN", 502, 1, 0},
		{"budget", "REJECTED", 402, 0, 0}, {"revoked-route", "RELEASED", 409, 0, 0},
		{"changed-target", "RELEASED", 409, 0, 0},
		{"settlement-unavailable", "SETTLE_PENDING", 503, 1, 2},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			f.mode = tc.mode
			out := f.request(managedSpeechBody, "speech-"+tc.mode)
			assert.Equal(t, tc.httpCode, out.Code, out.Body.String())
			assert.NotEqual(t, "audio/wav", out.Header().Get("Content-Type"))
			assert.Equal(t, tc.sends, f.sends)
			assert.Equal(t, tc.settles, f.settles)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call).Error)
			assert.Equal(t, tc.status, call.Status)
		})
	}
}

func TestManagedBailianSpeechPreservesZeroAndFrozenTariff(t *testing.T) {
	for _, tc := range []struct{ mode, quota string }{{"zero", "0"}, {"changed-price", "30"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			f.mode = tc.mode
			out := f.request(managedSpeechBody, "speech-"+tc.mode)
			require.Equal(t, 200, out.Code, out.Body.String())
			assert.Equal(t, tc.quota, f.settlement.NativeQuota)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
		})
	}
}

func TestManagedBailianSpeechRejectsMalformedRequestBeforeFunding(t *testing.T) {
	f := newManagedSpeechFixture(t)
	for _, body := range []string{
		`{"model":"voice","input":{"text":"hi","voice":"Cherry","unknown":true}}`,
		`{"model":"voice","input":{"text":"hi","text":"changed","voice":"Cherry"}}`,
		`{"model":"voice","input":{"text":"hi","voice":"Cherry"},"payer":"other"}`,
		`{"model":"voice","input":{"text":"` + strings.Repeat("x", 601) + `","voice":"Cherry"}}`,
	} {
		out := f.request(body, "speech-invalid")
		assert.Equal(t, 400, out.Code, out.Body.String())
	}
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
}

func TestManagedBailianSpeechRequiresExplicitCharacterTariffAndReservation(t *testing.T) {
	for _, missing := range []string{"price", "reservation"} {
		t.Run(missing, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			if missing == "price" {
				require.NoError(t, billing_setting.SetModelNativePricing(nil))
			} else {
				require.NoError(t, billing_setting.SetModelNativeReservationLimits(nil))
			}
			catalog, err := readProviderCatalog(context.Background(), f.adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
			require.NoError(t, err)
			require.Len(t, catalog.Models, 1)
			assert.False(t, catalog.Models[0].Probe, "legacy per-request ModelPrice must not imply a character tariff")
			out := f.request(managedSpeechBody, "speech-missing-config")
			assert.Equal(t, 503, out.Code)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
		})
	}
}

func TestManagedBailianSpeechCatalogDisablesUnsupportedAudioLimits(t *testing.T) {
	for _, limits := range []string{`{"formats":["mp3"]}`, `{"max_audio_seconds":0.5}`, `{"max_audio_seconds":600.5}`} {
		t.Run(limits, func(t *testing.T) {
			f := newManagedSpeechFixture(t)
			metadata := `{"operations":["bailian.audio.speech.create"],"input_modalities":["text"],"output_modalities":["audio"],"media_limits":` + limits + `}`
			require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata", metadata).Error)
			catalog, err := readProviderCatalog(context.Background(), f.adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
			require.NoError(t, err)
			require.Len(t, catalog.Models, 1)
			assert.False(t, catalog.Models[0].Probe)
			assert.False(t, catalog.Models[0].CapabilityMetadata.Operations[0].Executable)
			assert.Equal(t, "native_speech_target_unavailable", catalog.Models[0].CapabilityMetadata.Operations[0].DisabledReason)
			out := f.request(managedSpeechBody, "speech-unsupported-limits")
			assert.Equal(t, 503, out.Code)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
		})
	}
}
