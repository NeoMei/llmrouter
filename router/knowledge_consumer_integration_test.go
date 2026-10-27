package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const knowledgeConsumerPin = "f5adb9eac166e7d3da7dceed7aa50049d4c4db1f"

var knowledgeFixtureText = []string{
	"冷却规范：阀门必须关闭。 Cooling manual specifies valve closure.",
	"检修规范：阀门检查周期为七天。 Maintenance interval is seven days.",
}

type knowledgeRequestEvidence struct {
	Surface       string `json:"surface"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Phase         string `json:"phase"`
	Member        string `json:"member,omitempty"`
	Authorization bool   `json:"authorization_present"`
	Status        int    `json:"status"`
}

// Opt-in only: the script builds the unmodified pinned consumer in an isolated
// module. Ordinary go test neither downloads nor builds that external module.
func TestKnowledgePinnedConsumerIntegration(t *testing.T) {
	enableNativeCompatibility(t)
	binary := os.Getenv("LLMROUTER_KNOWLEDGE_CONSUMER_BINARY")
	if binary == "" {
		t.Skip("set LLMROUTER_KNOWLEDGE_CONSUMER_BINARY via scripts/certify-knowledge-consumer.sh")
	}
	require.True(t, filepath.IsAbs(binary))
	binaryBytes, err := os.ReadFile(binary)
	require.NoError(t, err)
	binaryHash := fmt.Sprintf("%x", sha256.Sum256(binaryBytes))
	if expected := os.Getenv("LLMROUTER_KNOWLEDGE_CONSUMER_SHA256"); expected != "" {
		require.Equal(t, expected, binaryHash)
	}
	evidence := os.Getenv("LLMROUTER_KNOWLEDGE_EVIDENCE")
	if evidence == "" {
		evidence = t.TempDir()
	}
	require.True(t, filepath.IsAbs(evidence))
	require.NoError(t, os.MkdirAll(evidence, 0700))
	// Keep raw child failures as well as successes. The consumer refuses existing
	// database/summary files; every run receives a fresh OS-temp directory.
	childDir, err := os.MkdirTemp("", "llmrouter-knowledge-consumer-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(childDir)) })
	var ledgerMu sync.Mutex
	var ledger []knowledgeRequestEvidence
	t.Cleanup(func() {
		ledgerMu.Lock()
		defer ledgerMu.Unlock()
		data, e := common.Marshal(ledger)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(filepath.Join(evidence, "request-ledger.json"), data, 0600))
	})
	document := knowledgeFixturePDF()
	documentPath := filepath.Join(childDir, "fixture.pdf")
	require.NoError(t, os.WriteFile(documentPath, document, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "fixture.pdf"), document, 0600))
	var imageBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	require.NoError(t, png.Encode(&imageBytes, img))
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "fixture.png"), imageBytes.Bytes(), 0600))

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gateway.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.OCRLogReceipt{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.OCRResource{}, &model.OCRResourceGrant{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldCache, oldRedis, oldConsume, oldRetry, oldTranslate := common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes = false, false, true, 1
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	require.NoError(t, i18n.Init())
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage = oldCache, oldRedis, oldConsume, oldRetry, oldTranslate
		require.NoError(t, sqlDB.Close())
	})
	ratio_setting.InitRatioSettings()
	oldRatio, oldPrice, oldGroup := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"text-embedding-v4":1,"gte-rerank-v2":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"PaddleOCR-VL-1.6":0.001}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroup))
	})
	settings := system_setting.GetFetchSetting()
	oldFetch := *settings
	*settings = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
	t.Cleanup(func() { *settings = oldFetch })
	service.InitHttpClient()
	service.InitTokenEncoders()
	user := model.User{Username: uuid.NewString(), AffCode: uuid.NewString(), Group: "default", Status: common.UserStatusEnabled, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: strings.ReplaceAll(uuid.NewString(), "-", ""), Name: "ephemeral consumer fixture", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000, ModelLimitsEnabled: true, ModelLimits: "PaddleOCR-VL-1.6,text-embedding-v4,gte-rerank-v2"}
	require.NoError(t, db.Create(&token).Error)
	var ocrChannel, embeddingChannel model.Channel
	var supplier *httptest.Server
	supplier = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := knowledgeRequestEvidence{Surface: "supplier", Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization") != "", Status: 200}
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer A":
			entry.Member = "A"
		case "Bearer B":
			entry.Member = "B"
		case "Bearer R":
			entry.Member = "R"
		}
		defer func() { ledgerMu.Lock(); ledger = append(ledger, entry); ledgerMu.Unlock() }()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/ocr/jobs":
			entry.Phase = "ocr-create"
			assert.Equal(t, "POST", r.Method)
			if !assert.NoError(t, r.ParseMultipartForm(1<<20)) {
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			assert.Equal(t, "PaddleOCR-VL-1.6", r.FormValue("model"))
			assert.JSONEq(t, `{"markdownIgnoreLabels":["header","header_image","footer","footer_image","number","footnote","aside_text"],"useDocOrientationClassify":false,"useDocUnwarping":false,"useLayoutDetection":true,"useChartRecognition":false,"useSealRecognition":true,"useOcrForImageBlock":false,"mergeTables":true,"relevelTitles":true,"restructurePages":true,"layoutShapeMode":"auto","promptLabel":"ocr","layoutNms":true,"repetitionPenalty":1,"temperature":0,"topP":1,"minPixels":147384,"maxPixels":2822400}`, r.FormValue("optionalPayload"))
			file, header, e := r.FormFile("file")
			if !assert.NoError(t, e) {
				w.WriteHeader(400)
				return
			}
			actual, e := io.ReadAll(file)
			file.Close()
			assert.NoError(t, e)
			assert.Equal(t, document, actual)
			assert.Equal(t, "task10-fixture.pdf", header.Filename)
			if auth == "Bearer A" {
				entry.Status = 429
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"code":429,"msg":"fixture limit"}`)
				return
			}
			assert.Equal(t, "Bearer B", auth)
			// Before accepted reply / first owned poll: move B from index 1 to 0.
			// Poll must follow its frozen identity, not the old position or key blob.
			if !assert.NoError(t, db.Model(&ocrChannel).Update("key", "B\nA").Error) {
				w.WriteHeader(500)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"jobId":"private-fixture-job"}}`)
		case "/api/v2/ocr/jobs/private-fixture-job":
			entry.Phase = "ocr-poll"
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "Bearer B", auth)
			fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":%q}}}`, supplier.URL+"/fixture.jsonl")
		case "/fixture.jsonl":
			entry.Phase = "jsonl-fetch"
			assert.Equal(t, "GET", r.Method)
			assert.Empty(t, auth)
			var pages []any
			for i, text := range knowledgeFixtureText {
				images := map[string]string{}
				markdown := text
				box := []int{120, 240, 480, 320}
				if i == 0 {
					images["images/diagram.png"] = supplier.URL + "/fixture.png"
					markdown += "\n\n![fixture](images/diagram.png)"
					box = []int{60, 80, 540, 160}
				}
				pages = append(pages, map[string]any{"markdown": map[string]any{"text": markdown, "images": images}, "prunedResult": map[string]any{"page_index": i, "width": 600, "height": 800, "parsing_res_list": []any{map[string]any{"block_label": "text", "block_content": text, "block_bbox": box}}}})
			}
			raw, e := common.Marshal(map[string]any{"result": map[string]any{"layoutParsingResults": pages}})
			assert.NoError(t, e)
			w.Write(append(raw, '\n'))
		case "/fixture.png":
			entry.Phase = "image-fetch"
			assert.Equal(t, "GET", r.Method)
			assert.Empty(t, auth)
			w.Header().Set("Content-Type", "image/png")
			w.Write(imageBytes.Bytes())
		case "/compatible-mode/v1/embeddings":
			assert.Equal(t, "POST", r.Method)
			var body struct {
				Model      string   `json:"model"`
				Input      []string `json:"input"`
				Dimensions int      `json:"dimensions"`
				Encoding   string   `json:"encoding_format"`
			}
			if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
				w.WriteHeader(400)
				return
			}
			assert.Equal(t, "text-embedding-v4", body.Model)
			assert.Equal(t, 1024, body.Dimensions)
			assert.Equal(t, "float", body.Encoding)
			entry.Phase = "document-embedding"
			usage := 20
			if len(body.Input) == 1 {
				entry.Phase = "query-embedding"
				usage = 10
				assert.Equal(t, []string{"阀门维护规范"}, body.Input)
			} else {
				assert.Len(t, body.Input, 2)
			}
			if auth == "Bearer A" {
				entry.Status = 429
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"message":"fixture limit","type":"rate_limit_error","code":"rate_limit"}}`)
				return
			}
			assert.Equal(t, "Bearer B", auth)
			var vectors []dto.EmbeddingResponseItem
			for i, text := range body.Input {
				vector := make([]float64, 1024)
				switch {
				case strings.Contains(text, knowledgeFixtureText[0]):
					vector[0] = 1
				case strings.Contains(text, knowledgeFixtureText[1]):
					vector[1] = 1
				case text == "阀门维护规范":
					vector[0] = 0.6
					vector[1] = 0.8
				default:
					t.Errorf("unexpected embedding input %q", text)
				}
				vectors = append(vectors, dto.EmbeddingResponseItem{Index: i, Embedding: vector})
			}
			if len(vectors) == 2 {
				vectors[0], vectors[1] = vectors[1], vectors[0]
			}
			raw, e := common.Marshal(dto.EmbeddingResponse{Model: body.Model, Data: vectors, Usage: dto.Usage{PromptTokens: usage, TotalTokens: usage}})
			assert.NoError(t, e)
			w.Write(raw)
		case "/api/v1/services/rerank/text-rerank/text-rerank":
			entry.Phase = "rerank"
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "Bearer R", auth)
			var body struct {
				Model string `json:"model"`
				Input struct {
					Query     string   `json:"query"`
					Documents []string `json:"documents"`
				} `json:"input"`
				Parameters struct {
					TopN            int   `json:"top_n"`
					ReturnDocuments *bool `json:"return_documents"`
				} `json:"parameters"`
			}
			if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
				w.WriteHeader(400)
				return
			}
			assert.Equal(t, "gte-rerank-v2", body.Model)
			assert.Equal(t, "阀门维护规范", body.Input.Query)
			if assert.Len(t, body.Input.Documents, 2) {
				assert.Contains(t, body.Input.Documents[0], knowledgeFixtureText[1])
				assert.Contains(t, body.Input.Documents[1], knowledgeFixtureText[0])
			}
			assert.Equal(t, 2, body.Parameters.TopN)
			if assert.NotNil(t, body.Parameters.ReturnDocuments) {
				assert.False(t, *body.Parameters.ReturnDocuments)
			}
			fmt.Fprint(w, `{"output":{"results":[{"index":1,"relevance_score":0.95},{"index":0,"relevance_score":0.2}]},"usage":{"total_tokens":40}}`)
		default:
			entry.Status = 404
			t.Errorf("unexpected supplier request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer supplier.Close()
	for _, spec := range []struct {
		name                string
		kind                int
		key, metadata, wire string
	}{
		{"PaddleOCR-VL-1.6", constant.ChannelTypePaddleOCR, "A\nB", `{"operations":["ocr.jobs.create","ocr.jobs.retrieve"],"input_modalities":["image","document"],"output_modalities":["text"],"async":true,"streaming":false}`, `["paddleocr_jobs"]`},
		{"text-embedding-v4", constant.ChannelTypeAli, "A\nB", `{"operations":["embeddings.create"],"input_modalities":["text"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text/query-document-v1"}}`, `["openai_chat"]`},
		{"gte-rerank-v2", constant.ChannelTypeAli, "R", `{"operations":["rerank.create"]}`, `["openai_chat"]`},
	} {
		channel := model.Channel{Type: spec.kind, Name: spec.name, Key: spec.key, BaseURL: &supplier.URL, Models: spec.name, Group: "default", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: strings.Contains(spec.key, "\n"), MultiKeyMode: constant.MultiKeyModePolling}}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{ChannelId: channel.Id, Group: "default", Model: spec.name, Enabled: true}).Error)
		route := model.ClientModelRoute{PublicId: spec.name, Enabled: true, Metadata: spec.metadata}
		require.NoError(t, db.Create(&route).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{RouteId: route.Id, ChannelId: channel.Id, UpstreamModel: spec.name, BillingModel: spec.name, PreferredWires: spec.wire, Enabled: true, Weight: 1}).Error)
		if spec.kind == constant.ChannelTypePaddleOCR {
			ocrChannel = channel
		}
		if spec.name == "text-embedding-v4" {
			embeddingChannel = channel
		}
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Next()
		entry := knowledgeRequestEvidence{Surface: "gateway", Method: c.Request.Method, Path: c.Request.URL.Path, Authorization: c.GetHeader("Authorization") != "", Status: c.Writer.Status(), Phase: "authenticated-model"}
		if strings.HasPrefix(entry.Path, "/api/ocr/jobs/") {
			entry.Phase = "granted-download"
			assert.False(t, entry.Authorization)
			assert.NotEmpty(t, c.Query("token"))
		}
		ledgerMu.Lock()
		ledger = append(ledger, entry)
		ledgerMu.Unlock()
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	billing := &ocr.Billing{OCRBillingStore: model.NewOCRBillingStore(db, nil)}
	defer billing.Close()
	svc, err := ocr.NewService(db, billing, ocr.Config{PublicOrigin: server.URL, GrantSecret: bytes.Repeat([]byte{7}, 32), Objects: objects, SupplierHTTP: supplier.Client(), FetchHTTP: service.GetSSRFProtectedHTTPClient()})
	require.NoError(t, err)
	runtime := &ocr.Runtime{DB: db, Billing: billing, Service: svc}
	SetRelayRouter(engine)
	SetOCRRouter(engine, &controller.OCRHandler{Runtime: runtime})
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	args := []string{"-gateway-url", server.URL, "-platform-token", token.Key, "-database", filepath.Join(childDir, "consumer.sqlite"), "-summary", filepath.Join(childDir, "summary.json"), "-documentpath", documentPath}
	commandEvidence := map[string]any{"binary": binary, "binary_sha256": binaryHash, "consumer_pin": knowledgeConsumerPin, "arguments": []string{"-gateway-url", server.URL, "-platform-token", "<ephemeral fixture token>", "-database", args[5], "-summary", args[7], "-documentpath", documentPath}, "parent_deadline_seconds": 180}
	raw, err := common.Marshal(commandEvidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "child-command.json"), raw, 0600))
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "child.stdout"), stdout.Bytes(), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "child.stderr"), stderr.Bytes(), 0600))
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "child.exit"), []byte(fmt.Sprintln(exitCode)), 0600))
	summaryBytes, summaryErr := os.ReadFile(filepath.Join(childDir, "summary.json"))
	if summaryErr == nil {
		require.NoError(t, os.WriteFile(filepath.Join(evidence, "child-summary.json"), summaryBytes, 0600))
	}
	if databaseBytes, databaseErr := os.ReadFile(filepath.Join(childDir, "consumer.sqlite")); databaseErr == nil {
		require.NoError(t, os.WriteFile(filepath.Join(evidence, "consumer.sqlite"), databaseBytes, 0600))
	}
	require.NoError(t, runErr, "actual pinned consumer failed; stdout=%s stderr=%s", stdout.String(), stderr.String())
	require.NoError(t, summaryErr)
	var summary struct {
		Schema         int    `json:"schema_version"`
		Pin            string `json:"consumer_pin"`
		Revision       string `json:"build_revision"`
		DocumentHash   string `json:"document_sha256"`
		Width          int    `json:"vector_width"`
		DocumentWidths []int  `json:"document_vector_widths"`
		QueryWidth     int    `json:"query_vector_width"`
		Images         []struct {
			SHA   string `json:"sha256"`
			Bytes int    `json:"bytes"`
		} `json:"images"`
		Winner struct {
			ID       string `json:"chunk_id"`
			Content  string `json:"content"`
			Start    int    `json:"start"`
			End      int    `json:"end"`
			Locators []struct {
				Page    int       `json:"page"`
				BBox    []float64 `json:"bbox"`
				Quote   string    `json:"quote"`
				Mapping string    `json:"mapping"`
			} `json:"source_locators"`
		} `json:"winner"`
		Markdown     string  `json:"markdown"`
		WinningIndex int     `json:"winning_rerank_index"`
		WinningScore float64 `json:"winning_rerank_score"`
		VectorHit    bool    `json:"winner_vector_hit"`
		KeywordHit   bool    `json:"winner_keyword_hit"`
	}
	require.NoError(t, common.Unmarshal(summaryBytes, &summary))
	assert.Equal(t, 1, summary.Schema)
	assert.Equal(t, knowledgeConsumerPin, summary.Pin)
	assert.Equal(t, knowledgeConsumerPin, summary.Revision)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(document)), summary.DocumentHash)
	require.Len(t, summary.Images, 1)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(imageBytes.Bytes())), summary.Images[0].SHA)
	assert.Equal(t, imageBytes.Len(), summary.Images[0].Bytes)
	assert.Equal(t, []int{1024, 1024}, summary.DocumentWidths)
	assert.Equal(t, 1024, summary.Width)
	assert.Equal(t, 1024, summary.QueryWidth)
	assert.True(t, summary.VectorHit)
	assert.True(t, summary.KeywordHit)
	assert.Equal(t, 1, summary.WinningIndex)
	assert.Equal(t, 0.95, summary.WinningScore)
	require.Len(t, summary.Winner.Locators, 1)
	locator := summary.Winner.Locators[0]
	assert.Equal(t, 1, locator.Page)
	assert.Equal(t, []float64{0.1, 0.1, 0.9, 0.2}, locator.BBox)
	assert.Equal(t, knowledgeFixtureText[0], locator.Quote)
	assert.Equal(t, "exact", locator.Mapping)
	require.GreaterOrEqual(t, summary.Winner.Start, 0)
	require.Greater(t, summary.Winner.End, summary.Winner.Start)
	require.LessOrEqual(t, summary.Winner.End, len([]rune(summary.Markdown)))
	assert.Equal(t, string([]rune(summary.Markdown)[summary.Winner.Start:summary.Winner.End]), summary.Winner.Content)
	var jobs []model.OCRJob
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	job := jobs[0]
	assert.Equal(t, "private-fixture-job", job.ProviderJobID)
	assert.NotEqual(t, job.ID, job.ProviderJobID)
	assert.Equal(t, token.Id, job.TokenID)
	assert.Equal(t, user.Id, job.UserID)
	assert.Equal(t, "success", job.Decision)
	assert.Equal(t, "done", job.ProviderState)
	assert.Equal(t, 2, job.SubmitAttempts)
	assert.Equal(t, 500, job.Quota)
	var current model.Channel
	require.NoError(t, db.First(&current, ocrChannel.Id).Error)
	assert.Equal(t, "B\nA", current.Key)
	refs, _, err := model.ChannelAccountRefs(ctx, db, &current)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	var binding model.OCRAccountBinding
	require.NoError(t, common.UnmarshalJsonStr(job.AccountBinding, &binding))
	assert.Equal(t, refs[0].MemberID, binding.Ref.MemberID)
	assert.Equal(t, 1, binding.Ref.KeyIndex, "B was selected at its original index")
	assert.Equal(t, 0, refs[0].KeyIndex, "B now occupies the first position")
	require.NotNil(t, binding.Owner)
	assert.Equal(t, binding.Ref.MemberID, binding.Owner.MemberID)
	assert.Equal(t, binding.Owner.MemberID, job.MemberID)
	raw, err = common.Marshal(binding)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "ocr-binding.json"), raw, 0600))
	// Explicitly drain twice and repeat an owned status read: neither may replay
	// money, submission, provider polling after readiness, or ordinary OCR logs.
	require.NoError(t, runtime.RunAuditBatch(ctx, 32))
	require.NoError(t, runtime.RunAuditBatch(ctx, 32))
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v2/ocr/jobs/"+job.ID, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 99430, user.Quota)
	assert.Equal(t, 570, user.UsedQuota)
	assert.Equal(t, 4, user.RequestCount)
	assert.Equal(t, 99430, token.RemainQuota)
	assert.Equal(t, 570, token.UsedQuota)
	var logs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Order("quota").Find(&logs).Error)
	require.Len(t, logs, 4)
	var charges []int
	ocrLogs := 0
	for _, log := range logs {
		charges = append(charges, log.Quota)
		if log.ModelName == "PaddleOCR-VL-1.6" {
			ocrLogs++
			assert.Equal(t, 500, log.Quota)
		}
	}
	assert.Equal(t, []int{10, 20, 40, 500}, charges)
	assert.Equal(t, 1, ocrLogs)
	var operations []model.OCROperation
	require.NoError(t, db.Where("job_id = ?", job.ID).Find(&operations).Error)
	require.Len(t, operations, 3)
	operationKinds := make(map[string]bool)
	for _, op := range operations {
		assert.False(t, operationKinds[op.Kind], "one receipt per funding component")
		operationKinds[op.Kind] = true
		assert.Equal(t, "reserve", op.Phase)
		assert.Equal(t, 500, op.Amount)
		if op.Kind == "subscription" {
			// Intent creation includes all funding components. Wallet-only billing
			// never executes this dormant subscription intent.
			assert.Equal(t, "prepared", op.State)
			assert.Zero(t, op.SQLAppliedAt)
		} else {
			assert.Contains(t, []string{"wallet", "token"}, op.Kind)
			assert.Equal(t, "applied", op.State)
			assert.Greater(t, op.SQLAppliedAt, int64(0))
		}
	}
	assert.Equal(t, map[string]bool{"subscription": true, "wallet": true, "token": true}, operationKinds)
	var events []model.OCRAuditEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Kind)
	assert.Greater(t, events[0].ProjectedAt, int64(0))
	var receipts []model.OCRLogReceipt
	require.NoError(t, db.Find(&receipts).Error)
	require.Len(t, receipts, 1)
	assert.Equal(t, events[0].ID, receipts[0].EventID)
	var states []model.UpstreamAccountState
	require.NoError(t, db.Where("reason = ?", "rate_limit").Find(&states).Error)
	assert.Len(t, states, 2)
	current = model.Channel{}
	require.NoError(t, db.First(&current, embeddingChannel.Id).Error)
	assert.Equal(t, "A\nB", current.Key)
	assert.Equal(t, "text-embedding-v4", current.Models)
	ledgerMu.Lock()
	snapshot := append([]knowledgeRequestEvidence(nil), ledger...)
	ledgerMu.Unlock()
	var sends []string
	downloads := 0
	for _, entry := range snapshot {
		if entry.Surface == "supplier" {
			sends = append(sends, entry.Phase+":"+entry.Member)
		}
		if entry.Surface == "gateway" && entry.Phase == "granted-download" {
			downloads++
			assert.Equal(t, 200, entry.Status)
		}
	}
	assert.Equal(t, []string{"ocr-create:A", "ocr-create:B", "ocr-poll:B", "jsonl-fetch:", "image-fetch:", "document-embedding:A", "document-embedding:B", "query-embedding:B", "rerank:R"}, sends)
	assert.Equal(t, 2, downloads)
	raw, err = common.Marshal(map[string]any{"document_sha256": summary.DocumentHash, "image_sha256": summary.Images[0].SHA, "binary_sha256": binaryHash, "wallet": user.Quota, "user_used": user.UsedQuota, "request_count": user.RequestCount, "token_remaining": token.RemainQuota, "token_used": token.UsedQuota, "charges": charges, "ocr_operations": operations, "ocr_audit_events": events, "ocr_log_receipts": receipts, "ocr_provider_polls": 1, "grant_downloads": downloads, "scope": "actual pinned clients + SQLite vec0/FTS5; fixture chunk projection"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(evidence, "acceptance.json"), raw, 0600))
}

// A real two-page PDF with a CJK Type0 font, page boxes matching supplier layout,
// and the same red 2x2 diagram as the downloaded PNG. OCR itself is a fixture.
func knowledgeFixturePDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 9 0 R >> >> /Contents 7 0 R >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 5 0 R >> >> /Contents 8 0 R >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> >>",
	}
	for i, text := range knowledgeFixtureText {
		var encoded strings.Builder
		for _, r := range utf16.Encode([]rune(text)) {
			fmt.Fprintf(&encoded, "%04X", r)
		}
		x, y := 60, 700
		if i == 1 {
			x, y = 120, 540
		}
		content := fmt.Sprintf("BT /F1 12 Tf %d %d Td <%s> Tj ET\n", x, y, encoded.String())
		if i == 0 {
			content += "q 40 0 0 40 60 580 cm /Im1 Do Q\n"
		}
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}
	pixels := strings.Repeat("\xff\x00\x00", 4)
	objects = append(objects, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(pixels), pixels))
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}
