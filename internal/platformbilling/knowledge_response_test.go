package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func responseFixture(t *testing.T, op string) (*Session, string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/response.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Call{}, &Attempt{}))
	key := "fixture-key"
	sum := sha256.Sum256([]byte(key))
	p := knowledgePolicy()
	p.PublicModel = "public"
	p.ChannelKeySHA256 = hex.EncodeToString(sum[:])
	p.RateIDs = map[string]string{"CREDIT/6": "rate"}
	request := `{"model":"public","query":"q","documents":["first","second"],"top_n":1,"return_documents":true}`
	if op == "embedding" {
		p.WireOperation = "embeddings.create"
		p.ChannelType = constant.ChannelTypeOpenAI
		p.UpstreamModel = "upstream-embed"
		p.HardInputTokens = 100
		p.MaxItemTokens = 40
		p.MaxBatch = 2
		p.Space = &dto.EmbeddingSpace{Provider: "openai", Model: p.UpstreamModel, Version: "v1", Dimensions: 3, InputSemantics: "text"}
		request = `{"model":"public","input":["first","second"]}`
	} else {
		p.HardInputTokens = 100
		p.MaxItemTokens = 40
		p.MaxBatch = 2
	}
	cfg := Config{Enabled: true, InstanceID: "default", MaxInputTokens: 100, MaxBodyBytes: 65536}
	var req operationRequest
	if op == "embedding" {
		req, err = parseKnowledgeEmbedding(cfg, p, []byte(request))
	} else {
		req, err = parseKnowledgeRerank(cfg, p, []byte(request), false)
	}
	require.NoError(t, err)
	price := metering.Price{Version: "metered_v2", Operation: op, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	rate := Rate{ID: "rate", Model: "public", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	rawRate, err := common.Marshal(rate)
	require.NoError(t, err)
	row := Call{CallID: "call", InstanceID: "default", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", TaskID: "task", Model: "public", Digest: "digest", Operation: op, WireOperation: p.WireOperation, Status: "UNKNOWN", MediaSnapshot: req.MediaSnapshot, RateSnapshot: string(rawRate), RateID: "rate", Asset: "CREDIT/6", FundingMode: "credits", ReservationQuantity: 100}
	require.NoError(t, db.Create(&row).Error)
	attempt := Attempt{CallID: row.CallID, ChannelID: p.ChannelID, Status: "DISPATCHED"}
	require.NoError(t, db.Create(&attempt).Error)
	return &Session{adapter: &Adapter{db: db, config: cfg}, call: row, attempt: attempt}, key, p.UpstreamModel
}

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, errors.New("interrupted") }
func (failedBody) Close() error             { return nil }

func TestKnowledgeResponseBoundToOriginalAttempt(t *testing.T) {
	for _, op := range []string{"embedding", "rerank"} {
		t.Run(op, func(t *testing.T) {
			s, key, upstream := responseFixture(t, op)
			body := `{"model":"upstream-embed","data":[{"index":1,"embedding":[0.4,0.5,0.6]},{"index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`
			if op == "rerank" {
				body = `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			}
			response := func(body string) *http.Response {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
			}
			got, err := validateKnowledgeResponse(context.Background(), s, 1, upstream, key, response(body))
			require.NoError(t, err)
			assert.Equal(t, s.call.CallID, got.callID)
			assert.Equal(t, s.attempt.ID, got.attemptID)
			assert.Equal(t, "confirmed", got.usage.State)
			assert.Equal(t, "3", got.usage.Measurements[0].Quantity.Value)
			if op == "embedding" {
				assert.Less(t, strings.Index(string(got.body), `"index":0`), strings.Index(string(got.body), `"index":1`))
			}
			_, err = validateKnowledgeResponse(context.Background(), s, 1, upstream, "wrong-key", response(body))
			assert.Error(t, err)
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("credential_id", "other-credential").Error)
			_, err = validateKnowledgeResponse(context.Background(), s, 1, upstream, key, response(body))
			assert.Error(t, err)
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("credential_id", s.call.CredentialID).Error)
			s.attempt.ID++
			_, err = validateKnowledgeResponse(context.Background(), s, 1, upstream, key, response(body))
			assert.Error(t, err)
		})
	}
}

func TestKnowledgeResponseRejectsInvalidAndIncomplete(t *testing.T) {
	for _, tc := range []struct{ op, body string }{
		{"embedding", `{"model":"other","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":0,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":101}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,null]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3,"total_tokens":4}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3,"prompt_tokens":3}}`},
		{"embedding", `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[0,0,0]}],"usage":{"prompt_tokens":3,"total_tokens":3,"completion_tokens":123}}`},
		{"rerank", `{"output":{"results":[{"index":2,"relevance_score":0.9}]},"usage":{"total_tokens":3}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":null,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":0.9}]},"usage":{"total_tokens":3}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"wrong"}}]},"usage":{"total_tokens":3}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3,"total_tokens":3}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":101}}`},
		{"rerank", `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":"3"}}`},
	} {
		t.Run(tc.op+"/"+tc.body, func(t *testing.T) {
			s, key, upstream := responseFixture(t, tc.op)
			_, err := validateKnowledgeResponse(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))})
			assert.Error(t, err)
		})
	}
	s, key, upstream := responseFixture(t, "embedding")
	_, err := validateKnowledgeResponse(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: failedBody{}})
	assert.Error(t, err)
	_, err = validateKnowledgeResponse(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", knowledgeResultMaxBytes+1)))})
	assert.Error(t, err)
	_, err = validateKnowledgeResponse(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("{}"))})
	assert.Error(t, err)
}
