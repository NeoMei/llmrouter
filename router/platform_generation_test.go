package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManagedNativeGenerationExactWire(t *testing.T) {
	enableUniHubBoundary(t)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	for _, tc := range []struct {
		name, path, wire, body, response string
		channel                          int
	}{
		{"chat", "/v1/chat/completions", "chat.completions.create", `{"model":"platform-test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"temperature":0,"stream":false,"parallel_tool_calls":false}`, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, constant.ChannelTypeOpenAI},
		{"responses", "/v1/responses", "responses.create", `{"model":"platform-test","input":"hi","max_output_tokens":10,"temperature":0,"background":false,"store":false,"parallel_tool_calls":false}`, `{"id":"r","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, constant.ChannelTypeOpenAI},
		{"compact", "/v1/responses/compact", "responses.compact", `{"model":"platform-test","input":[{"type":"compaction","id":null,"encrypted_content":"opaque-history"},{"type":"function_call","call_id":"f","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"f","output":"done"}],"instructions":null}`, `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[{"type":"compaction","id":"cmp_item","encrypted_content":"returned-opaque"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, constant.ChannelTypeOpenAI},
		{"messages", "/v1/messages", "claude.messages.create", `{"model":"platform-test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"temperature":0,"top_k":0,"stream":false}`, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, constant.ChannelTypeAnthropic},
		{"gemini", "/v1beta/models/platform-test:generateContent", "gemini.generate", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10,"temperature":0,"topK":0,"thinkingConfig":{"thinkingBudget":0,"includeThoughts":false}}}`, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`, constant.ChannelTypeGemini},
	} {
		for _, variant := range []string{"text", "catalog", "vision", "vision_codec", "vision_jpeg", "vision_jpeg_codec", "vision_mismatch_codec", "vision_unknownformat", "document", "vision_nativebound", "document_nativebound", "vision_policybound", "document_policybound", "vision_format", "vision_count", "vision_bytes", "vision_modality", "document_modality", "vision_output", "stream", "history", "history_stream", "zero", "missing", "partial", "conflict", "overcap", "wrongquery", "wrongwire", "pathmodel", "headersoverride", "state_enabled", "artifact_missing", "duplicate_usage", "cache_reasoning", "cache_unpriced", "details_missing", "split_input_overcap", "split_output_overcap", "wrongendpoint", "unknown_usage", "vision_outputcap", "vision_contextcap", "previous_ref", "previous_ref_enabled", "item_ref", "item_ref_enabled"} {
			if tc.name == "compact" && (variant == "stream" || variant == "history" || variant == "history_stream") {
				continue
			}
			if tc.name != "compact" && (variant == "state_enabled" || variant == "artifact_missing" || variant == "duplicate_usage" || variant == "cache_reasoning" || variant == "cache_unpriced" || variant == "details_missing" || variant == "split_input_overcap" || variant == "split_output_overcap" || variant == "wrongendpoint" || variant == "unknown_usage" || variant == "vision_outputcap" || variant == "vision_contextcap" || variant == "previous_ref" || variant == "previous_ref_enabled" || variant == "item_ref" || variant == "item_ref_enabled") {
				continue
			}
			if variant == "headersoverride" && tc.name != "messages" {
				continue
			}
			if (variant == "wrongquery" && tc.name != "gemini" && tc.name != "compact") || (variant == "pathmodel" && tc.name != "gemini") {
				continue
			}
			t.Run(tc.name+"/"+variant, func(t *testing.T) {
				tc := tc
				if tc.name == "compact" {
					t.Setenv("RESPONSES_STATE_ENABLED", "")
				}
				if variant == "state_enabled" || strings.HasSuffix(variant, "ref_enabled") {
					t.Setenv("RESPONSES_STATE_ENABLED", "true")
				}
				if tc.name == "compact" {
					tc.response = strings.Replace(tc.response, `"total_tokens":2`, `"total_tokens":2,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}`, 1)
				}
				mediaVariant := strings.HasPrefix(variant, "vision") || strings.HasPrefix(variant, "document")
				document := strings.HasPrefix(variant, "document")
				mediaDenied := mediaVariant && strings.Contains(variant, "_") && variant != "vision_codec" && variant != "vision_jpeg" && variant != "vision_jpeg_codec"
				mediaMIME := "image/png"
				if strings.HasPrefix(variant, "vision_jpeg") {
					mediaMIME = "image/jpeg"
				}
				if document {
					mediaMIME = "application/pdf"
				}
				denied := variant == "wrongquery" || variant == "wrongwire" || variant == "pathmodel" || strings.Contains(variant, "_ref")

				if variant == "wrongquery" {
					tc.path += "?key=caller-authority"
				}
				var wireBody map[string]any
				require.NoError(t, common.UnmarshalUnique([]byte(tc.body), &wireBody))
				if strings.HasPrefix(variant, "previous_ref") {
					wireBody["previous_response_id"] = "foreign-provider-response"
				}
				if strings.HasPrefix(variant, "item_ref") {
					wireBody["input"] = []any{map[string]any{"type": "item_reference", "id": "foreign-provider-item"}}
				}
				if mediaVariant {
					var img bytes.Buffer
					require.NoError(t, png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 3))))
					if mediaMIME == "image/jpeg" {
						img.Reset()
						require.NoError(t, jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil))
					}
					mime, data := mediaMIME, img.Bytes()
					if document {
						mime, data = "application/pdf", generationPDF()
					}
					encoded := base64.StdEncoding.EncodeToString(data)
					uri := "data:" + mime + ";base64," + encoded
					switch tc.name {
					case "chat":
						part := map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}}
						if document {
							part = map[string]any{"type": "file", "file": map[string]any{"filename": "fixture.pdf", "file_data": uri}}
						}
						wireBody["messages"] = []any{map[string]any{"role": "user", "content": []any{part}}}
					case "responses", "compact":
						part := map[string]any{"type": "input_image", "image_url": uri}
						if document {
							part = map[string]any{"type": "input_file", "filename": "fixture.pdf", "file_data": uri}
						}
						wireBody["input"] = []any{map[string]any{"role": "user", "content": []any{part}}}
					case "messages":
						kind := "image"
						if document {
							kind = "document"
						}
						wireBody["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": kind, "source": map[string]any{"type": "base64", "media_type": mime, "data": encoded}}}}}
					case "gemini":
						wireBody["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": encoded}}}}}
					}
				}
				if variant == "vision_count" || variant == "document_nativebound" {
					key, content := "messages", "content"
					if tc.name == "responses" || tc.name == "compact" {
						key = "input"
					}
					if tc.name == "gemini" {
						key, content = "contents", "parts"
					}
					msg := wireBody[key].([]any)[0].(map[string]any)
					parts := msg[content].([]any)
					msg[content] = append(parts, parts[0])
				}

				if variant == "history" || variant == "history_stream" {
					history := generationHistory(tc.name)
					tc.response = generationToolResponse(tc.name)
					require.NoError(t, common.UnmarshalUnique([]byte(history), &wireBody))
				}
				if variant == "stream" || variant == "history_stream" {
					if tc.name == "gemini" {
						tc.path = strings.Replace(tc.path, "generateContent", "streamGenerateContent?alt=sse", 1)
					} else {
						wireBody["stream"] = true
					}
					if variant == "history_stream" {
						tc.response = generationToolStream(tc.name, tc.response)
					} else {
						tc.response = generationStream(tc.name, tc.response)
					}
				}
				expectedStatus := "SETTLED"
				expectedSettles := 1
				if variant == "zero" || variant == "missing" || variant == "partial" || variant == "overcap" || variant == "conflict" {
					var response map[string]any
					require.NoError(t, common.UnmarshalUnique([]byte(tc.response), &response))
					key, input, output, total := "usage", "prompt_tokens", "completion_tokens", "total_tokens"
					if tc.name == "messages" {
						input, output = "input_tokens", "output_tokens"
					}
					if tc.name == "responses" || tc.name == "compact" {
						input, output = "input_tokens", "output_tokens"
					}
					if tc.name == "gemini" {
						key, input, output, total = "usageMetadata", "promptTokenCount", "candidatesTokenCount", "totalTokenCount"
					}
					usage := map[string]any{input: 0, output: 0, total: 0}
					if tc.name == "compact" {
						usage["input_tokens_details"] = map[string]any{"cached_tokens": 0, "cache_write_tokens": 0}
						usage["output_tokens_details"] = map[string]any{"reasoning_tokens": 0}
					}
					if tc.name == "messages" {
						delete(usage, total)
					}
					if variant == "partial" {
						delete(usage, output)
						delete(usage, total)
					}
					if variant == "conflict" {
						usage[input] = 1
						usage[output] = 1
						usage[total] = 1
					}
					if variant == "overcap" {
						usage[input] = 10001
						usage[total] = 10001
					}
					response[key] = usage
					if variant == "missing" {
						delete(response, key)
					}
					raw, e := common.Marshal(response)
					require.NoError(t, e)
					tc.response = string(raw)
					if variant == "conflict" && tc.name == "messages" {
						tc.response = strings.Replace(tc.response, `"input_tokens":1`, `"input_tokens":1,"input_tokens":2`, 1)
					}
					if variant != "zero" {
						expectedStatus = "UNKNOWN"
						expectedSettles = 0
					}
				}
				if variant == "artifact_missing" {
					tc.response = `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
					expectedStatus = "UNKNOWN"
					expectedSettles = 0
				}
				if variant == "duplicate_usage" {
					tc.response = strings.Replace(tc.response, `"input_tokens":1`, `"input_tokens":1,"input_tokens":2`, 1)
					expectedStatus = "UNKNOWN"
					expectedSettles = 0
				}
				if variant == "cache_reasoning" || variant == "cache_unpriced" || variant == "details_missing" || variant == "split_input_overcap" || variant == "split_output_overcap" {
					tc.response = `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[{"type":"compaction","id":"cmp_item","encrypted_content":"returned-opaque"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}`
				}
				if variant == "cache_unpriced" || variant == "details_missing" || variant == "split_input_overcap" || variant == "split_output_overcap" {
					expectedStatus = "UNKNOWN"
					expectedSettles = 0
				}
				if variant == "unknown_usage" {
					tc.response = strings.Replace(tc.response, `"cached_tokens":0`, `"cached_tokens":0,"unknown_tokens":1`, 1)
					expectedStatus = "UNKNOWN"
					expectedSettles = 0
				}
				if variant == "details_missing" {
					tc.response = strings.Replace(tc.response, `,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}`, "", 1)
					tc.response = strings.Replace(tc.response, `,"output_tokens_details":{"reasoning_tokens":1}`, "", 1)
				}
				if variant == "split_input_overcap" {
					tc.response = strings.ReplaceAll(tc.response, `"input_tokens":10`, `"input_tokens":12000`)
					tc.response = strings.ReplaceAll(tc.response, `"cached_tokens":2`, `"cached_tokens":3000`)
					tc.response = strings.ReplaceAll(tc.response, `"total_tokens":15`, `"total_tokens":12005`)
				}
				if variant == "split_output_overcap" {
					tc.response = strings.ReplaceAll(tc.response, `"output_tokens":5`, `"output_tokens":150`)
					tc.response = strings.ReplaceAll(tc.response, `"reasoning_tokens":1`, `"reasoning_tokens":80`)
					tc.response = strings.ReplaceAll(tc.response, `"total_tokens":15`, `"total_tokens":160`)
				}
				if variant == "pathmodel" {
					wireBody["model"] = "other"
				}
				raw, e := common.Marshal(wireBody)
				require.NoError(t, e)
				tc.body = string(raw)

				require.NoError(t, i18n.Init())
				gin.SetMode(gin.TestMode)
				db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
				require.NoError(t, e)
				require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
				oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
				model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled = db, db, true, false
				t.Cleanup(func() {
					perfmetrics.DrainRelaySamples()
					model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled = oldDB, oldLog, oldCache, oldRedis
				})
				sends, settles, reserves := 0, 0, 0
				var callID string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends++
					assert.Empty(t, r.Header.Get("X-Untrusted"))
					switch tc.name {
					case "messages":
						assert.Equal(t, "key", r.Header.Get("X-Api-Key"))
						assert.Equal(t, "2023-06-01", r.Header.Get("Anthropic-Version"))
						assert.Empty(t, r.Header.Get("Anthropic-Beta"))
					case "gemini":
						assert.Equal(t, "key", r.Header.Get("X-Goog-Api-Key"))
						if variant == "stream" || variant == "history_stream" {
							assert.Equal(t, "sse", r.URL.Query().Get("alt"))
						} else {
							assert.Empty(t, r.URL.RawQuery)
						}
					default:
						assert.Equal(t, "Bearer key", r.Header.Get("Authorization"))
					}
					var row platformbilling.Call
					require.NoError(t, db.First(&row, "call_id = ?", callID).Error)
					assert.Equal(t, "UNKNOWN", row.Status)
					var attempts []platformbilling.Attempt
					require.NoError(t, db.Where("call_id = ?", callID).Find(&attempts).Error)
					require.Len(t, attempts, 1)
					assert.Equal(t, "DISPATCHED", attempts[0].Status)
					raw, e := io.ReadAll(r.Body)
					require.NoError(t, e)
					expected := tc.body
					if (variant == "stream" || variant == "history_stream") && tc.name == "chat" {
						wireBody["stream_options"] = map[string]any{"include_usage": true}
						b, e := common.Marshal(wireBody)
						require.NoError(t, e)
						expected = string(b)
					}
					assert.JSONEq(t, expected, string(raw))
					w.Header().Set("Content-Type", "application/json")
					if variant == "stream" {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, tc.response)
				}))
				defer upstream.Close()
				op := "chat"
				if tc.name == "responses" || tc.name == "compact" {
					op = "responses"
				}
				price := metering.Price{Version: "metered_v2", Operation: op, Policy: "sum", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
				if variant == "cache_reasoning" || variant == "split_input_overcap" || variant == "split_output_overcap" {
					for _, key := range []string{"cache_read", "cache_write", "reasoning"} {
						price.Terms = append(price.Terms, metering.Term{Key: key, Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
					}
				}
				rate := platformbilling.Rate{ID: "rate", Model: "platform-test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
				var remote platformbilling.RemoteCall
				hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var out any
					switch r.URL.Path {
					case "/internal/v1/model-credentials/verify":
						allowed := tc.wire
						if variant == "wrongwire" {
							allowed = "embeddings.create"
						}
						out = platformbilling.Claims{Operations: map[string][]string{"platform-test": {allowed}}, ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "origin", Models: []string{"platform-test"}, CredentialKind: "api_key", InstanceID: "default", ExpiresAt: time.Now().Add(time.Hour)}
					case "/internal/v1/rates":
						out = []platformbilling.Rate{rate}
					case "/internal/v1/calls/reserve":
						reserves++
						var in map[string]string
						require.NoError(t, common.DecodeJson(r.Body, &in))
						callID = in["call_id"]
						remote = platformbilling.RemoteCall{InstanceID: "default", WireOperation: in["wire_operation"], CallID: callID, ActorID: "actor", AccountID: "account", OriginProductID: "origin", CredentialID: "credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
						out = remote
					case "/internal/v1/calls/" + callID + "/settle":
						settles++
						remote.Status = "SETTLED"
						out = remote
					case "/internal/v1/calls/" + callID + "/release":
						remote.Status = "RELEASED"
						out = remote
					default:
						t.Errorf("unexpected hub path %s", r.URL.Path)
						w.WriteHeader(404)
						return
					}
					b, e := common.Marshal(out)
					require.NoError(t, e)
					w.Write(b)
				}))
				defer hub.Close()
				require.NoError(t, db.Create(&model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}).Error)
				base := upstream.URL
				if variant == "wrongendpoint" {
					base += "/prefix"
				}
				require.NoError(t, db.Create(&model.Channel{Id: 1, Type: tc.channel, Key: "key", Status: common.ChannelStatusEnabled, Name: "platform", BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
				require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
				if variant == "catalog" || mediaVariant {
					preferred := map[string]string{"chat": "openai_chat", "responses": "responses", "messages": "anthropic", "gemini": "gemini", "compact": "responses"}[tc.name]
					limits := map[string]any{"max_pages": 2, "max_pixels": 100, "max_images": 2, "max_request_bytes": 10000, "formats": []string{"image/png", "application/pdf"}}
					switch variant {
					case "vision_codec":
						limits["formats"] = []string{"png", "application/pdf"}
					case "vision_jpeg":
						limits["formats"] = []string{"image/jpeg", "application/pdf"}
					case "vision_jpeg_codec":
						limits["formats"] = []string{"jpeg", "application/pdf"}
					case "vision_mismatch_codec":
						limits["formats"] = []string{"jpeg", "application/pdf"}
					case "vision_unknownformat":
						limits["formats"] = []string{"image/bmp"}
					case "vision_nativebound":
						limits["max_pixels"] = 5
					case "document_nativebound":
						limits["max_pages"] = 1
					case "vision_format":
						limits["formats"] = []string{"application/pdf"}
					case "vision_count":
						limits["max_images"] = 1
					case "vision_bytes":
						limits["max_request_bytes"] = 100
					}
					inputs, outputs := []string{"text", "image", "document"}, []string{"text"}
					if variant == "vision_modality" {
						inputs = []string{"text", "document"}
					}
					if variant == "document_modality" {
						inputs = []string{"text", "image"}
					}
					if variant == "vision_output" {
						outputs = []string{"image"}
					}
					maxOutput := 100
					if variant == "vision_outputcap" {
						maxOutput = 50
					}
					metadata, e := common.Marshal(map[string]any{"operations": []string{tc.wire}, "input_modalities": inputs, "output_modalities": outputs, "max_output_tokens": maxOutput, "media_limits": limits})
					require.NoError(t, e)
					require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "platform-test", DisplayName: "platform-test", Client: "all", Enabled: true, Metadata: string(metadata)}).Error)
					require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "platform-test", PreferredWires: `["` + preferred + `"]`, Enabled: true, Weight: 1}).Error)
				}
				if variant == "vision_contextcap" {
					require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 1).Update("context_window", 9999).Error)
				}

				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"platform-test":0}`))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
				model.InitChannelCache()
				service.InitHttpClient()
				service.InitTokenEncoders()
				config := platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 10000, MaxOutputTokens: 100, MaxBodyBytes: 65536, NativeInputPolicies: map[string]map[string]platformbilling.NativeInputPolicy{tc.wire: {"platform-test": {Version: "hard_context_v1", ProviderReference: "https://fixture.example/hard-context", ChannelType: tc.channel, UpstreamModel: "platform-test", HardInputTokens: 10000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 2, MaxPages: 2}}}, OperationRateIDs: map[string]map[string]map[string]string{op: {"CREDIT/6": {"platform-test": "rate"}}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{op: {"platform-test": {Policy: "sum"}}}}
				if tc.name == "responses" && variant == "catalog" {
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
				}
				if tc.name == "compact" {
					config.CompactPolicies = map[string]platformbilling.CompactPolicy{"platform-test": {Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: "platform-test", ChannelType: tc.channel, UpstreamModel: "platform-test", WireOperation: tc.wire, ChannelID: 1, EndpointPath: "/v1/responses/compact", HardInputTokens: 10000, HardOutputTokens: 100, ServiceTier: "auto", RateIDs: map[string]string{"CREDIT/6": "rate"}}}
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
				}
				policy := config.NativeInputPolicies[tc.wire]["platform-test"]
				if variant == "vision_policybound" {
					policy.MaxPixels = 5
				}
				if variant == "document_policybound" {
					policy.MaxDocuments = 0
				}
				config.NativeInputPolicies[tc.wire]["platform-test"] = policy
				adapter, e := platformbilling.New(db, config)
				require.NoError(t, e)
				if mediaVariant && !mediaDenied {
					catalog, err := readTechnicalCatalog(context.Background(), adapter, "superwagie")
					require.NoError(t, err)
					require.Len(t, catalog.Models, 1)
					require.True(t, catalog.Models[0].Operations[0].Available, catalog.Models[0].Operations[0].DisabledReason)
					require.LessOrEqual(t, int64(len(tc.body)), *catalog.Models[0].Capabilities.MediaLimits.MaxRequestBytes)
					if !document {
						assert.Equal(t, []string{mediaMIME, "application/pdf"}, catalog.Models[0].Capabilities.MediaLimits.Formats)
					}
				}
				engine := gin.New()
				SetManagedKeyDispatch(engine, adapter)
				send := func() *httptest.ResponseRecorder {
					r := httptest.NewRequest("POST", tc.path, bytes.NewBufferString(tc.body))
					r.Header.Set("Authorization", "Bearer uh_key_test")
					r.Header.Set("Idempotency-Key", "call")
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Anthropic-Version", "caller-version")
					r.Header.Set("Anthropic-Beta", "unpriced-feature")
					r.Header.Set("X-Untrusted", "caller-value")
					w := httptest.NewRecorder()
					engine.ServeHTTP(w, r)
					return w
				}
				if variant == "headersoverride" {
					settings := model_setting.GetClaudeSettings()
					old := settings.HeadersSettings
					settings.HeadersSettings = map[string]map[string][]string{"platform-test": {"anthropic-beta": {"uncertified"}}}
					t.Cleanup(func() { settings.HeadersSettings = old })
				}
				if variant == "vision_unknownformat" || variant == "vision_contextcap" || variant == "vision_outputcap" {
					catalog, err := readTechnicalCatalog(context.Background(), adapter, "superwagie")
					require.NoError(t, err)
					require.Len(t, catalog.Models, 1)
					assert.False(t, catalog.Models[0].Operations[0].Available)
					if variant == "vision_unknownformat" {
						assert.Empty(t, catalog.Models[0].Capabilities.MediaLimits.Formats)
					}
				}
				w := send()
				if mediaDenied {
					assert.Equal(t, 400, w.Code, w.Body.String())
					assert.Zero(t, sends)
					assert.Zero(t, settles)
					if strings.HasSuffix(variant, "policybound") {
						assert.Zero(t, reserves)
					}
					return
				}
				if variant == "headersoverride" || variant == "wrongendpoint" {
					assert.GreaterOrEqual(t, w.Code, 400)
					assert.Zero(t, sends)
					return
				}
				if denied {
					assert.Equal(t, 400, w.Code, w.Body.String())
					assert.Zero(t, sends)
					assert.Zero(t, reserves)
					return
				}
				if (variant == "conflict" && tc.name == "messages") || variant == "artifact_missing" || variant == "duplicate_usage" {
					require.GreaterOrEqual(t, w.Code, 400)
				} else {
					require.Equal(t, 200, w.Code, w.Body.String())
				}
				if variant == "history" || variant == "history_stream" {
					if tc.name == "gemini" {
						assert.Contains(t, w.Body.String(), "returned-signature")
					} else {
						assert.Contains(t, w.Body.String(), "f2")
					}
				}
				if tc.name == "compact" && expectedStatus == "SETTLED" {
					assert.Contains(t, w.Body.String(), "returned-opaque")
				}
				assert.Equal(t, 1, sends)
				assert.Equal(t, expectedSettles, settles)
				assert.Equal(t, 1, reserves)
				w = send()
				assert.Equal(t, 409, w.Code, w.Body.String())
				assert.Equal(t, 1, sends)
				assert.Equal(t, 1, reserves)
				var user model.User
				require.NoError(t, db.First(&user, 1).Error)
				assert.Equal(t, 12345, user.Quota)
				assert.Equal(t, 7, user.UsedQuota)
				var call platformbilling.Call
				require.NoError(t, db.First(&call, "call_id = ?", callID).Error)
				assert.Equal(t, expectedStatus, call.Status)
				if variant == "cache_reasoning" {
					var u metering.Usage
					require.NoError(t, common.Unmarshal([]byte(call.Evidence), &u))
					values := map[string]string{}
					for _, m := range u.Measurements {
						values[m.Key] = m.Quantity.Value
					}
					assert.Equal(t, map[string]string{"input": "5", "output": "4", "cache_read": "2", "cache_write": "3", "reasoning": "1"}, values)
				}

				assert.Equal(t, tc.wire, call.WireOperation)
				_, e = adapter.Receipt(context.Background(), "uh_key_test", call.TaskID, callID)
				require.NoError(t, e)
				assert.Equal(t, 1, sends)
			})
		}
	}
}

// A deterministic, valid single-page PDF with bounded content and xref.
func generationPDF() []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources << >> /Contents 4 0 R >>", "<< /Length 0 >>\nstream\n\nendstream"} {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes()
}
func generationStream(protocol, terminal string) string {
	switch protocol {
	case "chat":
		return "data: " + terminal + "\n\ndata: [DONE]\n\n"
	case "responses":
		return "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + terminal + "}\n\n"
	case "messages":
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	default:
		return "data: " + terminal + "\n\n"
	}
}

func generationHistory(protocol string) string {
	switch protocol {
	case "chat":
		return `{"model":"platform-test","max_tokens":10,"messages":[{"role":"assistant","tool_calls":[{"id":"f","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"f","content":"done"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"},"strict":false}}],"parallel_tool_calls":false}`
	case "responses":
		return `{"model":"platform-test","max_output_tokens":10,"input":[{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"opaque"},{"type":"function_call","call_id":"f","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"f","output":"done"},{"type":"custom_tool_call","call_id":"c","name":"shell","input":"pwd"},{"type":"custom_tool_call_output","call_id":"c","output":"ok"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":false},{"type":"custom","name":"shell","format":{"type":"text"}}],"background":false,"parallel_tool_calls":false}`
	case "messages":
		return `{"model":"platform-test","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"history","signature":"opaque"},{"type":"redacted_thinking","data":"opaque"},{"type":"tool_use","id":"f","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"f","content":"done","is_error":false}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"stream":false}`
	default:
		return `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}},"thoughtSignature":"opaque"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"answer":"done"},"willContinue":false}}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}],"generationConfig":{"maxOutputTokens":10,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"low"}}}`
	}
}

func generationToolResponse(protocol string) string {
	switch protocol {
	case "chat":
		return `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"f2","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	case "responses":
		return `{"id":"r","object":"response","status":"completed","output":[{"id":"f2","type":"function_call","call_id":"call2","name":"lookup","arguments":"{}","status":"completed"},{"id":"c2","type":"custom_tool_call","call_id":"custom2","name":"shell","input":"pwd","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	case "messages":
		return `{"id":"m","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"returned-signature"},{"type":"tool_use","id":"f2","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
	default:
		return `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}},"thoughtSignature":"returned-signature"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	}
}
func generationToolStream(protocol, terminal string) string {
	switch protocol {
	case "responses":
		var response map[string]any
		_ = common.UnmarshalUnique([]byte(terminal), &response)
		var events strings.Builder
		for i, item := range response["output"].([]any) {
			raw, _ := common.Marshal(map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
			events.WriteString("event: response.output_item.done\ndata: " + string(raw) + "\n\n")
		}
		return events.String() + generationStream(protocol, terminal)

	case "chat":
		return "data: " + `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"f2","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: " + `{"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\ndata: [DONE]\n\n"
	case "messages":
		return "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\nevent: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"f2","name":"lookup","input":{}}}` + "\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	default:
		return generationStream(protocol, terminal)
	}
}
