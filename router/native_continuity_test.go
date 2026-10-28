package router

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type nativeContinuityFixture struct {
	sequenceBase             int64
	beforeReserve            func()
	completed                chan string
	t                        *testing.T
	db                       *gorm.DB
	config                   platformbilling.Config
	adapter                  *platformbilling.Adapter
	gateway                  *httptest.Server
	supplier                 *httptest.Server
	hub                      *httptest.Server
	sends, reserves, settles atomic.Int64
	mu                       sync.Mutex
	responseMode             string
	sentBodies               []string
	remote                   map[string]platformbilling.RemoteCall
	key, pool                bool
	grant                    platformbilling.Claims
}

func newNativeContinuityFixture(t *testing.T, key, pool bool, live ...map[string]string) *nativeContinuityFixture {
	t.Helper()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "native.db")
	if len(live) > 0 {
		dbPath = live[0]["database"]
	}
	db, e := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
	model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled = db, db, true, false
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
		model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled = oldDB, oldLog, oldCache, oldRedis
		sql, _ := db.DB()
		sql.Close()
	})
	f := &nativeContinuityFixture{completed: make(chan string, 1000), t: t, db: db, key: key, pool: pool, remote: map[string]platformbilling.RemoteCall{}}
	f.grant = platformbilling.Claims{Operations: map[string][]string{"native-model": {"responses.create", "responses.compact"}, "native-other": {"responses.create", "responses.compact"}}, ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", Models: []string{"native-model", "native-other"}, CredentialKind: "task", InstanceID: "default", TaskID: "root-task", ExpiresAt: time.Now().Add(time.Hour)}
	if key {
		f.grant.CredentialKind = "api_key"
		f.grant.TaskID = ""
	}
	f.supplier = httptest.NewUnstartedServer(http.HandlerFunc(f.supply))
	if len(live) > 0 {
		f.supplier.Listener.Close()
		listener, e := net.Listen("tcp", live[0]["supplier"])
		require.NoError(t, e)
		f.supplier.Listener = listener
	}
	f.supplier.Start()
	t.Cleanup(f.supplier.Close)
	price := metering.Price{Version: "metered_v2", Operation: "responses", Policy: "sum", Terms: []metering.Term{}}
	for _, term := range []string{"input", "output", "cache_read", "cache_write", "reasoning"} {
		price.Terms = append(price.Terms, metering.Term{Key: term, Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
	}
	rates := []platformbilling.Rate{}
	for _, m := range []string{"native-model", "native-other"} {
		rates = append(rates, platformbilling.Rate{ID: "rate-" + m, Model: m, Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price})
	}
	f.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			out = f.grant
		case r.URL.Path == "/internal/v1/rates":
			out = rates
		case r.URL.Path == "/internal/v1/calls/reserve":
			f.reserves.Add(1)
			if f.beforeReserve != nil {
				f.beforeReserve()
			}
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			var rate platformbilling.Rate
			for _, v := range rates {
				if v.ID == in["rate_id"] {
					rate = v
				}
			}
			remote := platformbilling.RemoteCall{InstanceID: "default", WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: f.grant.ActorID, AccountID: f.grant.AccountID, OriginProductID: f.grant.OriginProductID, CredentialID: f.grant.ID, RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
			f.remote[remote.CallID] = remote
			out = remote
		case strings.HasSuffix(r.URL.Path, "/settle"), strings.HasSuffix(r.URL.Path, "/release"):
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-2]
			remote := f.remote[id]
			remote.Status = "RELEASED"
			if strings.HasSuffix(r.URL.Path, "/settle") {
				f.settles.Add(1)
				remote.Status = "SETTLED"
				remote.Charged = "15"
			}
			f.remote[id] = remote
			out = remote
		default:
			t.Errorf("unexpected hub path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		raw, e := common.Marshal(out)
		require.NoError(t, e)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(f.hub.Close)
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}).Error)
	base := f.supplier.URL
	channel := model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "key", Status: common.ChannelStatusEnabled, Name: "native", BaseURL: &base, Models: "native-model,native-other", Group: "platform"}
	if pool {
		channel.Key = "key\nkey-two"
		channel.ChannelInfo.IsMultiKey = true
	}
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&channel).Error)
	for i, m := range []string{"native-model", "native-other"} {
		require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.Ability{Group: "platform", Model: m, ChannelId: 1, Enabled: true}).Error)
		require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ClientModelRoute{Id: i + 1, PublicId: m, DisplayName: m, Client: "all", Enabled: true, Metadata: `{"operations":["responses.create","responses.compact"],"input_modalities":["text"],"output_modalities":["text"],"max_output_tokens":100}`}).Error)
		require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ClientModelTarget{Id: i + 1, RouteId: i + 1, ChannelId: 1, UpstreamModel: m, PreferredWires: `["responses"]`, Enabled: true, Weight: 1}).Error)
	}
	oldModelRatio, oldGroupRatio := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldModelRatio))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroupRatio))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"native-model":0,"native-other":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	namespace, e := platformbilling.NativeHistoryNamespace(base+"/v1/responses", "")
	require.NoError(t, e)
	f.config = platformbilling.Config{Enabled: true, Origin: f.hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 10000, MaxOutputTokens: 100, MaxBodyBytes: 65536, NativeInputPolicies: map[string]map[string]platformbilling.NativeInputPolicy{}, CompactPolicies: map[string]platformbilling.CompactPolicy{}, OperationRateIDs: map[string]map[string]map[string]string{"responses": {"CREDIT/6": {}}}, NativeHistory: &platformbilling.NativeHistoryConfig{ActiveKeyVersion: "v1", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{0x38}, 32)}, TTLSeconds: 3600, Capacity: 10000, Policies: map[string]platformbilling.NativeHistoryPolicy{}}}
	for _, m := range []string{"native-model", "native-other"} {
		for _, wire := range []string{"responses.create", "responses.compact"} {
			if f.config.NativeInputPolicies[wire] == nil {
				f.config.NativeInputPolicies[wire] = map[string]platformbilling.NativeInputPolicy{}
			}
			f.config.NativeInputPolicies[wire][m] = platformbilling.NativeInputPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/context", ChannelType: 1, UpstreamModel: m, HardInputTokens: 10000, MaxBytes: 10000, MaxPixels: 100, MaxPages: 2}
		}
		f.config.CompactPolicies[m] = platformbilling.CompactPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: m, ChannelType: 1, UpstreamModel: m, WireOperation: "responses.compact", ChannelID: 1, EndpointPath: "/v1/responses/compact", HardInputTokens: 10000, HardOutputTokens: 100, ServiceTier: "auto", RateIDs: map[string]string{"CREDIT/6": "rate-" + m}}
		f.config.OperationRateIDs["responses"]["CREDIT/6"][m] = "rate-" + m
		f.config.NativeHistory.Policies[m] = platformbilling.NativeHistoryPolicy{Version: "fixture-v1", ChannelID: 1, Namespace: namespace, UpstreamModel: m, DefaultStored: true}
	}
	if len(live) > 0 {
		f.config.Origin = live[0]["origin"]
		f.config.ServiceToken = live[0]["service"]
		for m := range f.config.OperationRateIDs["responses"]["CREDIT/6"] {
			f.config.CompactPolicies[m] = platformbilling.CompactPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: m, ChannelType: 1, UpstreamModel: m, WireOperation: "responses.compact", ChannelID: 1, EndpointPath: "/v1/responses/compact", HardInputTokens: 10000, HardOutputTokens: 100, ServiceTier: "auto", RateIDs: map[string]string{"CNY/6": "native-rate"}}
		}
		f.config.OperationRateIDs = map[string]map[string]map[string]string{"responses": {"CNY/6": {"native-model": "native-rate"}}}
	}
	if len(live) > 0 && db.Migrator().HasTable(&platformbilling.Attempt{}) {
		require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&f.sequenceBase).Error)
	}
	f.restart()
	t.Cleanup(func() { f.gateway.Close() })
	return f
}
func (f *nativeContinuityFixture) restart() {
	f.t.Helper()
	if f.gateway != nil {
		f.gateway.Close()
	}
	a, e := platformbilling.New(f.db, f.config)
	require.NoError(f.t, e)
	f.adapter = a
	engine := gin.New()
	if f.key {
		SetManagedKeyDispatch(engine, a)
	} else {
		SetPlatformRouter(engine, a)
	}
	engine.GET("/__fixture/status", func(c *gin.Context) {
		var user model.User
		require.NoError(f.t, f.db.First(&user, 1).Error)
		var references int64
		require.NoError(f.t, f.db.Table("platform_native_history_references").Count(&references).Error)
		var calls []platformbilling.Call
		require.NoError(f.t, f.db.Find(&calls).Error)
		c.JSON(200, gin.H{"provider_calls": f.sends.Load(), "quota": user.Quota, "used_quota": user.UsedQuota, "request_count": user.RequestCount, "references": references, "calls": calls})
	})
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("Idempotency-Key")
		engine.ServeHTTP(w, r)
		if r.Method == "POST" {
			f.completed <- id
		}
	}))
}
func (f *nativeContinuityFixture) supply(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.sends.Add(1) + f.sequenceBase
	raw, e := io.ReadAll(r.Body)
	require.NoError(f.t, e)
	f.sentBodies = append(f.sentBodies, string(raw))
	assert.Equal(f.t, "Bearer key", r.Header.Get("Authorization"))
	terminal := fmt.Sprintf(`{"id":"response-%d","object":"response","status":"completed","output":[{"type":"message","id":"item-%d","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic local provider","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`, n, n)
	if strings.HasSuffix(r.URL.Path, "/compact") {
		terminal = `{"id":"compact-only","created_at":1700000000,"object":"response.compaction","output":[{"type":"compaction","id":"compact-item","encrypted_content":"opaque-returned"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`
	}
	switch f.responseMode {
	case "failed":
		terminal = strings.Replace(terminal, `"status":"completed"`, `"status":"failed"`, 1)
	case "duplicate":
		terminal = strings.Replace(terminal, `"id":`, `"id":"duplicate", "id":`, 1)
	case "echo":
		terminal = strings.Replace(terminal, fmt.Sprintf(`"item-%d"`, n), `"echo-item"`, 1)
	case "missing":
		terminal = strings.Replace(terminal, `"input_tokens":10,`, "", 1)
	case "store_false":
		terminal = strings.Replace(terminal, `"status":"completed"`, `"store":false,"status":"completed"`, 1)
	case "refusal":
		terminal = strings.Replace(terminal, `{"type":"output_text","text":"synthetic local provider","annotations":[]}`, `{"type":"refusal","refusal":"Cannot assist with that request"}`, 1)
	}

	if f.responseMode == "duplicate_items" || f.responseMode == "oversized_items" || f.responseMode == "invalid_id" || f.responseMode == "input_echo_role" {
		var response map[string]any
		require.NoError(f.t, common.UnmarshalUnique([]byte(terminal), &response))
		output := response["output"].([]any)
		item := output[0].(map[string]any)
		switch f.responseMode {
		case "duplicate_items":
			output = append(output, item)
		case "oversized_items":
			output = []any{}
			for i := 0; i < 129; i++ {
				output = append(output, map[string]any{"type": "message", "id": fmt.Sprintf("item-%d", i), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "x"}}})
			}
		case "invalid_id":
			item["id"] = "id\ninvalid"
		case "input_echo_role":
			item["role"] = "user"
		}
		response["output"] = output
		encoded, e := common.Marshal(response)
		require.NoError(f.t, e)
		terminal = string(encoded)
	}
	var request map[string]any
	require.NoError(f.t, common.UnmarshalUnique(raw, &request))
	if request["input"] == "first tools" {
		var response map[string]any
		require.NoError(f.t, common.UnmarshalUnique([]byte(terminal), &response))
		response["output"] = []any{map[string]any{"id": "function-item", "type": "function_call", "call_id": "f", "name": "lookup", "arguments": "{}", "status": "completed"}, map[string]any{"id": "custom-item", "type": "custom_tool_call", "call_id": "c", "name": "shell", "input": "pwd", "status": "completed"}}
		encoded, e := common.Marshal(response)
		require.NoError(f.t, e)
		terminal = string(encoded)
	}

	if request["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		stream := generationStream("responses", terminal)
		if request["input"] == "first tools" {
			stream = generationToolStream("responses", terminal)
		}
		switch f.responseMode {
		case "partial":
			stream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"uncompleted\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
		case "repeated_terminal":
			stream += generationStream("responses", terminal)
		}
		fmt.Fprint(w, stream)
	} else {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, terminal)
	}
}
func (f *nativeContinuityFixture) send(id, path, body string, want int) string {
	f.t.Helper()
	if f.key {
		path = "/v1" + path
	} else {
		path = "/platform/v1" + path
	}
	r, e := http.NewRequest("POST", f.gateway.URL+path, strings.NewReader(body))
	require.NoError(f.t, e)
	token := "uh_model_fixture"
	if f.key {
		token = "uh_key_fixture"
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", id)
	r.Header.Set("X-UniHub-Task-ID", "root-task")
	resp, e := http.DefaultClient.Do(r)
	require.NoError(f.t, e)
	defer resp.Body.Close()
	raw, e := io.ReadAll(resp.Body)
	require.NoError(f.t, e)
	select {
	case completed := <-f.completed:
		require.Equal(f.t, id, completed)
	case <-time.After(10 * time.Second):
		f.t.Fatal("gateway handler completion missing")
	}
	require.Equal(f.t, want, resp.StatusCode, string(raw))
	return string(raw)
}
func nativeBody(input, extra string) string {
	return `{"model":"native-model","input":` + input + `,"max_output_tokens":100` + extra + `}`
}
func TestManagedNativeResponsesContinuity(t *testing.T) {
	for _, flag := range []string{"", "true", "false"} {
		for _, key := range []bool{false, true} {
			for _, pool := range []bool{false, true} {
				t.Run(fmt.Sprintf("flag_%s/key_%t/pool_%t", flag, key, pool), func(t *testing.T) {
					t.Setenv("RESPONSES_STATE_ENABLED", flag)
					f := newNativeContinuityFixture(t, key, pool)
					first := nativeBody(`"first tools"`, `,"store":true,"previous_response_id":null,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"custom","name":"shell","format":{"type":"text"}}]`)
					if flag == "true" {
						first = strings.TrimSuffix(first, "}") + `,"stream":true}`
					}
					f.send("create", "/responses", first, 200)
					var creating platformbilling.Call
					require.NoError(t, f.db.First(&creating).Error)
					root := creating.TaskID
					if !key {
						assert.Equal(t, "root-task", root)
					}
					second := nativeBody(`[{"type":"function_call_output","call_id":"f","output":"done"},{"type":"custom_tool_call_output","call_id":"c","output":"custom"}]`, `,"previous_response_id":"response-1"`)
					f.send("continue", "/responses", second, 200)
					f.send("item", "/responses", nativeBody(`[{"id":"item-2"}]`, `,"stream":true`), 200)
					compact := `{"model":"native-model","input":[{"type":"item_reference","id":"item-3"}],"previous_response_id":"response-1"}`
					f.send("compact", "/responses/compact", compact, 200)
					f.send("inline", "/responses", nativeBody(`[{"type":"compaction","id":null,"encrypted_content":"opaque-returned"}]`, `,"store":false`), 200)
					require.Equal(t, int64(5), f.sends.Load())
					require.Equal(t, int64(5), f.reserves.Load())
					require.Equal(t, int64(5), f.settles.Load())
					var rows []platformbilling.Call
					require.NoError(t, f.db.Order("call_id").Find(&rows).Error)
					require.Len(t, rows, 5)
					for _, row := range rows {
						if row.NativeObservation == "" && row.WireOperation == "responses.create" && key {
							assert.NotEqual(t, root, row.TaskID)
						} else {
							assert.Equal(t, root, row.TaskID)
						}
						assert.Equal(t, "actor", row.ActorID)
						assert.Equal(t, "account", row.AccountID)
						assert.Equal(t, "SETTLED", row.Status)
						assert.NotEmpty(t, row.ContinuitySnapshot)
					}
					var count int64
					require.NoError(t, f.db.Table("platform_native_history_references").Count(&count).Error)
					assert.Equal(t, int64(7), count)
					f.restart()
					f.send("continue", "/responses", second, 409)
					f.send("continue", "/responses", nativeBody(`"changed"`, `,"previous_response_id":"response-1"`), 409)
					f.send("resume", "/responses", nativeBody(`"resume"`, `,"previous_response_id":"response-1"`), 200)
					for _, id := range []string{"compact-only", "response-5", "foreign-response"} {
						before := f.reserves.Load()
						f.send("deny-"+id, "/responses", nativeBody(`"deny"`, fmt.Sprintf(`,"previous_response_id":%q`, id)), 400)
						assert.Equal(t, before, f.reserves.Load())
					}
					if pool {
						require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "key-two\nkey").Error)
						model.InitChannelCache()
						f.send("reordered", "/responses", nativeBody(`"same member"`, `,"previous_response_id":"response-1"`), 200)
					}
					before := f.reserves.Load()
					require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "rotated-key").Error)
					model.InitChannelCache()
					f.send("rotated", "/responses", nativeBody(`"deny"`, `,"previous_response_id":"response-1"`), 400)
					assert.Equal(t, before, f.reserves.Load())
					f.send("continue", "/responses", second, 409)
					var user model.User
					require.NoError(t, f.db.First(&user, 1).Error)
					assert.Equal(t, 12345, user.Quota)
					assert.Equal(t, 7, user.UsedQuota)
					assert.Equal(t, 3, user.RequestCount)
				})
			}
		}
	}
}

func TestManagedNativeHistoryOwnerAndDestination(t *testing.T) {
	f := newNativeContinuityFixture(t, false, false)
	f.send("create", "/responses", nativeBody(`"first"`, ""), 200)
	original := f.grant
	for _, field := range []string{"actor", "account", "product", "instance", "root"} {
		t.Run(field, func(t *testing.T) {
			f.mu.Lock()
			f.grant = original
			switch field {
			case "actor":
				f.grant.ActorID = "foreign"
			case "account":
				f.grant.AccountID = "foreign"
			case "product":
				f.grant.OriginProductID = "foreign"
			case "instance":
				f.grant.InstanceID = "foreign"
			case "root":
				f.grant.TaskID = "foreign"
			}
			f.mu.Unlock()
			f.send("deny-"+field, "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 400)
			assert.Equal(t, int64(1), f.reserves.Load())
		})
	}
	f.mu.Lock()
	f.grant = original
	f.grant.ID = "refreshed"
	f.mu.Unlock()
	f.send("refresh", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 200)
	other := strings.Replace(nativeBody(`"next"`, `,"previous_response_id":"response-1"`), "native-model", "native-other", 1)
	f.send("incompatible", "/responses", other, 400)
	f.send("independent", "/responses", strings.Replace(nativeBody(`"new"`, ""), "native-model", "native-other", 1), 200)
	p := f.config.NativeHistory.Policies["native-other"]
	p.CompatibleSources = []platformbilling.NativeHistorySource{{PublicModel: "native-model", UpstreamModel: "native-model"}}
	f.config.NativeHistory.Policies["native-other"] = p
	f.restart()
	f.send("compatible", "/responses", other, 200)
}

func TestManagedNativeHistoryStorageTerminals(t *testing.T) {
	for _, mode := range []string{"store_false", "explicit_false", "failed", "duplicate", "echo", "missing", "partial", "repeated_terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeContinuityFixture(t, false, false)
			f.mu.Lock()
			f.responseMode = mode
			f.mu.Unlock()
			extra := ",\"store\":true"
			input := `"x"`
			if mode == "explicit_false" {
				extra = `,"store":false`
			}
			if mode == "partial" || mode == "repeated_terminal" {
				extra += `,"stream":true`
			}
			if mode == "echo" {
				input = `[{"type":"message","id":"echo-item","role":"user","content":"old"}]`
			}
			body := nativeBody(input, extra)
			want := 200
			if mode == "failed" || mode == "duplicate" || mode == "echo" || mode == "repeated_terminal" {
				want = 502
			}
			f.send("create", "/responses", body, want)
			var n int64
			require.NoError(t, f.db.Table("platform_native_history_references").Count(&n).Error)
			if mode == "missing" {
				assert.Equal(t, int64(2), n)
				var row platformbilling.Call
				require.NoError(t, f.db.First(&row, "call_id = ?", "create").Error)
				assert.Equal(t, "UNKNOWN", row.Status)
			} else {
				assert.Zero(t, n)
			}
			f.send("create", "/responses", body, 409)
			assert.Equal(t, int64(1), f.sends.Load())
		})
	}
}

func TestManagedNativeHistoryTargetChangeAndSendRace(t *testing.T) {
	for _, phase := range []string{"before_admission", "after_reserve"} {
		for _, change := range []string{"key", "endpoint", "organization", "disabled", "mapping"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f := newNativeContinuityFixture(t, false, true)
				f.send("create", "/responses", nativeBody(`"first"`, ""), 200)
				mutate := func() {
					values := map[string]any{}
					switch change {
					case "key":
						values["key"] = "replacement"
					case "endpoint":
						values["base_url"] = f.supplier.URL + "/changed"
					case "organization":
						values["open_ai_organization"] = "foreign-org"
					case "disabled":
						values["status"] = common.ChannelStatusManuallyDisabled
					case "mapping":
						values["model_mapping"] = `{"native-model":"changed-model"}`
					}
					require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Updates(values).Error)
				}
				want := 400
				if phase == "before_admission" {
					mutate()
				} else {
					f.mu.Lock()
					f.beforeReserve = mutate
					f.mu.Unlock()
					want = 502
					if change == "key" || change == "mapping" {
						want = 500
					}
					if change == "disabled" {
						want = 403
					}
				}
				f.send("race", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), want)
				assert.Equal(t, int64(1), f.sends.Load())
				var attempts int64
				require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Where("call_id = ?", "race").Count(&attempts).Error)
				assert.Zero(t, attempts)
				if phase == "before_admission" {
					assert.Equal(t, int64(1), f.reserves.Load())
				} else {
					assert.Equal(t, int64(2), f.reserves.Load())
					var row platformbilling.Call
					require.NoError(t, f.db.First(&row, "call_id = ?", "race").Error)
					assert.Equal(t, "RELEASED", row.Status)
				}
			})
		}
	}
}

func TestManagedNativeHistoryRegistrationFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			f := newNativeContinuityFixture(t, false, false)
			require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register("test_native_history_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "platform_native_history_references" {
					tx.AddError(errors.New("isolated registry write refusal"))
				}
			}))
			t.Cleanup(func() { _ = f.db.Callback().Create().Remove("test_native_history_failure") })
			body := nativeBody(`"first"`, fmt.Sprintf(`,"stream":%t`, stream))
			raw := f.send("failure", "/responses", body, 502)
			assert.NotContains(t, raw, "response.completed")
			assert.NotContains(t, raw, "synthetic local provider")
			var n int64
			require.NoError(t, f.db.Table("platform_native_history_references").Count(&n).Error)
			assert.Zero(t, n)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "failure").Error)
			assert.NotEmpty(t, call.NativeObservation)
			assert.Equal(t, "SETTLED", call.Status)
			assert.Contains(t, call.Evidence, `"state":"confirmed"`)
			f.send("failure", "/responses", body, 409)
			assert.Equal(t, int64(1), f.sends.Load())
		})
	}
}

func TestManagedNativeHistoryDurableKeyScope(t *testing.T) {
	f := newNativeContinuityFixture(t, true, false)
	f.send("first", "/responses", nativeBody(`"first"`, ""), 200)
	f.send("second", "/responses", nativeBody(`"independent"`, ""), 200)
	f.send("mixed", "/responses", nativeBody(`[{"id":"item-2"}]`, `,"previous_response_id":"response-1"`), 400)
	assert.Equal(t, int64(2), f.reserves.Load())
	f.mu.Lock()
	f.grant.ID = "rotated-credential"
	f.mu.Unlock()
	f.send("foreign-key", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 400)
	assert.Equal(t, int64(2), f.reserves.Load())
}

func TestManagedNativeHistoryRefusalAndNullableStorage(t *testing.T) {
	f := newNativeContinuityFixture(t, false, false)
	f.mu.Lock()
	f.responseMode = "refusal"
	f.mu.Unlock()
	f.send("null-store", "/responses", nativeBody(`"first"`, `,"store":null`), 200)
	f.send("previous", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 200)
}

func TestManagedNativeHistoryExpiryAndRetainedKeys(t *testing.T) {
	f := newNativeContinuityFixture(t, false, false)
	body := nativeBody(`"first"`, "")
	f.send("create", "/responses", body, 200)
	f.config.NativeHistory.Keys["v2"] = bytes.Repeat([]byte{0x48}, 32)
	f.config.NativeHistory.ActiveKeyVersion = "v2"
	f.restart()
	f.send("rotated-index-key", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 200)
	delete(f.config.NativeHistory.Keys, "v1")
	f.restart()
	f.send("missing-retained-key", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 400)
	f.send("create", "/responses", body, 409)
	f.config.NativeHistory.Keys["v1"] = bytes.Repeat([]byte{0x38}, 32)
	f.restart()
	require.NoError(t, f.db.Table("platform_native_history_references").Where("1 = 1").Update("expires_ms", int64(1)).Error)
	f.send("expired", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 400)
	f.send("create", "/responses", body, 409)
	assert.Equal(t, int64(2), f.sends.Load())
	assert.Equal(t, int64(2), f.reserves.Load())
}

func TestManagedNativeHistoryCompletionBoundsAndCapacity(t *testing.T) {
	for _, mode := range []string{"duplicate_items", "oversized_items", "invalid_id", "input_echo_role", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeContinuityFixture(t, false, false)
			f.mu.Lock()
			f.responseMode = mode
			f.mu.Unlock()
			if mode == "capacity" {
				require.NoError(t, f.db.Table("platform_native_history_capacity").Where("id = ?", 1).Update("capacity", 1).Error)
				f.config.NativeHistory.Capacity = 1
				f.restart()
			}
			body := nativeBody(`"first"`, "")
			f.send("bounded", "/responses", body, 502)
			var n int64
			require.NoError(t, f.db.Table("platform_native_history_references").Count(&n).Error)
			assert.Zero(t, n)
			f.send("bounded", "/responses", body, 409)
			assert.Equal(t, int64(1), f.sends.Load())
		})
	}
}
func TestManagedNativeHistoryBeforeFundsContextAndIndependentModel(t *testing.T) {
	f := newNativeContinuityFixture(t, false, false)
	f.send("create", "/responses", nativeBody(`"first"`, ""), 200)
	require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 1).Update("context_window", 1).Error)
	f.send("context-too-small", "/responses", nativeBody(`"next"`, `,"previous_response_id":"response-1"`), 400)
	assert.Equal(t, int64(1), f.reserves.Load())
	delete(f.config.NativeHistory.Policies, "native-other")
	f.restart()
	f.send("unconfigured-independent", "/responses", strings.Replace(nativeBody(`"new task model"`, ""), "native-model", "native-other", 1), 200)
}
