package router

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Explicit opt-in local acceptance harness. UniHub and its ledger remain real;
// only the synthetic OpenAI provider is simulated. Never reads provider stores.
func TestPlatformLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_PLATFORM_E2E") != "1" {
		t.Skip("opt-in local UniHub acceptance fixture")
	}
	envFile := os.Getenv("UNIHUB_PLATFORM_E2E_ENV_FILE")
	require.True(t, filepath.IsAbs(envFile), "UNIHUB_PLATFORM_E2E_ENV_FILE must explicitly name an absolute local env file")
	values, e := godotenv.Read(envFile)
	require.NoError(t, e)
	token := values["MODEL_SERVICE_TOKEN"]
	require.GreaterOrEqual(t, len(token), 32, "MODEL_SERVICE_TOKEN must exist in local UniHub env")
	gin.SetMode(gin.ReleaseMode)
	require.NoError(t, i18n.Init())
	constant.StreamingTimeout = 30
	db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/gateway.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	model.LOG_DB = db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	common.RetryTimes = 1
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if common.DecodeJson(r.Body, &body) != nil {
			w.WriteHeader(400)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic local provider\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"unihub-synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic local provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer upstream.Close()
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "isolated-non-financial-principal", Status: common.UserStatusEnabled, Group: "platform", Quota: 0}).Error)
	base := upstream.URL
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "synthetic-not-a-provider-key", Status: common.ChannelStatusEnabled, Name: "isolated synthetic channel", BaseURL: &base, Models: "unihub-synthetic-model", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "unihub-synthetic-model", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"unihub-synthetic-model":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: "http://localhost:8088", ServiceToken: token, PrincipalID: 1, Group: "platform", MaxBodyBytes: 32768, MaxInputTokens: 65536, MaxOutputTokens: 8192, RateIDs: map[string]string{"unihub-synthetic-model": "local-e2e-chat-v1"}})
	require.NoError(t, e)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/platform/v1/chat/completions" {
			c.Next()
			return
		}
		reader := &platformFixtureCountReader{ReadCloser: c.Request.Body}
		c.Request.Body = reader
		writer := &platformFixtureResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = common.Unmarshal(writer.errorBody.Bytes(), &envelope)
		code := envelope.Error.Code
		switch code {
		case "invalid_model_credential", "request_too_large", "stable_call_and_task_required", "platform_billing_unavailable", "logical_call_already_exists", "logical_call_conflict":
		default:
			code = "none_or_unrecognized"
		}
		t.Logf("platform fixture request: declared_bytes=%d read_bytes=%d status=%d error_code=%s", c.Request.ContentLength, reader.count, writer.Status(), code)
	})
	engine.Use(gin.Recovery(), middlewareBodyCleanupForTest())
	SetPlatformRouter(engine, adapter)
	listener, e := net.Listen("tcp", "127.0.0.1:3001")
	require.NoError(t, e)
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stop := adapter.Start(ctx)
	defer stop()
	t.Log("isolated synthetic gateway ready at http://127.0.0.1:3001; real UniHub at localhost:8088; auto-stop 15 minutes")
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Minute):
	}
}

type platformFixtureCountReader struct {
	io.ReadCloser
	count int
}

func (r *platformFixtureCountReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.count += n
	return n, err
}

type platformFixtureResponseWriter struct {
	gin.ResponseWriter
	errorBody bytes.Buffer
}

func (w *platformFixtureResponseWriter) Write(p []byte) (int, error) {
	if w.Status() >= 400 && w.errorBody.Len()+len(p) <= 4096 {
		_, _ = w.errorBody.Write(p)
	}
	return w.ResponseWriter.Write(p)
}
