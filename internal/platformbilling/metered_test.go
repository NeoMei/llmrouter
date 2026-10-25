package platformbilling

import (
	"bytes"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeteredOperationValidation(t *testing.T) {
	cfg := Config{MaxBodyBytes: 8192, MaxInputTokens: 4096, MaxOutputTokens: 100}
	for _, tc := range []struct{ op, body string }{
		{"speech", `{"model":"test","input":"hello","voice":"alloy"}`},
		{"embedding", `{"model":"test","input":["a","b"]}`},
		{"responses", `{"model":"test","input":"hello","max_output_tokens":10}`},
		{"image_generation", `{"model":"test","prompt":"hello","n":2}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			req, err := parseOperation(cfg, tc.op, "application/json", []byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, "test", req.Model)
		})
	}
	for _, body := range []string{`{"model":"test","input":"a","input":"b"}`, `{"model":"test","input":"a","duration":123}`, `{"model":"test","input":[]}`} {
		_, err := parseOperation(cfg, "speech", "application/json", []byte(body))
		require.Error(t, err)
	}
}
func TestMeteredUsageWireOmitsLegacy(t *testing.T) {
	b, err := common.Marshal(Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: "0"}}}}})
	require.NoError(t, err)
	var v map[string]any
	require.NoError(t, common.Unmarshal(b, &v))
	require.Len(t, v, 1)
}

func meteredFixture(t *testing.T, op string) (*Adapter, *gorm.DB, *atomic.Int64) {
	t.Helper()
	var settles atomic.Int64
	rate := Rate{ID: "native", Model: "test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: op, Policy: "sum", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	if op == "ocr" {
		rate.Metered.Terms[0].Key = "requests"
		rate.Metered.Terms[0].Unit = "request"
	}
	var remote RemoteCall
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			account := "account"
			if in["token"] == "uh_model_other" {
				account = "other"
			}
			out = Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: account, OriginProductID: "origin", Models: []string{"test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
		case "/internal/v1/rates":
			out = []Rate{rate}
		case "/internal/v1/calls/reserve":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			remote = RemoteCall{CallID: in["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "origin", RequestDigest: in["request_digest"], Rate: rate, Status: "RESERVED", Held: in["amount"], CredentialID: "credential", ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Allocations: []Allocation{{BatchID: "batch", Amount: in["amount"]}}}
			out = remote
		case "/internal/v1/calls/call/settle":
			var in struct {
				Usage Usage `json:"usage"`
			}
			require.NoError(t, common.DecodeJson(r.Body, &in))
			require.NotNil(t, in.Usage.Metered)
			settles.Add(1)
			remote.Status = "SETTLED"
			remote.Charged = in.Usage.Metered.Measurements[0].Quantity.Value
			out = remote
		case "/internal/v1/calls/call/release":
			remote.Status = "RELEASED"
			out = remote
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		b, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	t.Cleanup(server.Close)
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/native.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: server.URL, ServiceToken: "service-service-service-service-token", Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	return a, db, &settles
}
func speechUsage(value string) metering.Usage {
	return metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: value}}}}
}
func TestMeteredZeroSettlementAndCanonicalDuplicate(t *testing.T) {
	a, db, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	body := []byte(`{"model":"test","input":"hello","voice":"alloy"}`)
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", body)
	require.NoError(t, e)
	_, e = a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"voice":"alloy", "input":"hello", "model":"test"}`))
	require.ErrorIs(t, e, ErrDuplicate)
	require.NoError(t, s.Dispatch(ctx, 1))
	require.Error(t, s.Dispatch(ctx, 1))
	require.NoError(t, s.ObserveMetering(ctx, speechUsage("0")))
	require.NoError(t, s.Finish(ctx))
	require.NoError(t, s.Finish(ctx))
	assert.EqualValues(t, 1, settles.Load())
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.Equal(t, "0", row.Charged)
	assert.NotEmpty(t, row.RateSnapshot)
	assert.NotEmpty(t, row.FundingSnapshot)
	_, e = a.Task(ctx, "uh_model_other", "task", "call")
	require.Error(t, e)
}
func TestMeteredConflictingEvidenceRetainsHold(t *testing.T) {
	a, db, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	require.NoError(t, s.ObserveMetering(ctx, speechUsage("2")))
	require.ErrorIs(t, s.ObserveMetering(ctx, speechUsage("3")), ErrConflict)
	require.ErrorIs(t, s.Finish(ctx), ErrConflict)
	assert.Zero(t, settles.Load())
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.True(t, row.EvidenceConflict)
	assert.Equal(t, "UNKNOWN", row.Status)
}
func TestMeteredAsyncBindingRecovery(t *testing.T) {
	a, db, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	require.NoError(t, s.BindTask(ctx, "provider-task"))
	require.ErrorIs(t, s.BindTask(ctx, "different-task"), ErrConflict)
	require.NoError(t, s.Finish(ctx))
	require.Zero(t, settles.Load())
	require.Error(t, a.ObserveTask(ctx, "call", "different-task", speechUsage("1")))
	restarted, e := New(db, a.config)
	require.NoError(t, e)
	require.NoError(t, restarted.ObserveTask(ctx, "call", "provider-task", speechUsage("1")))
	assert.EqualValues(t, 1, settles.Load())
}
func TestMultipartDigestIgnoresBoundaryAndRejectsDuplicate(t *testing.T) {
	makeBody := func(boundary string, duplicate bool) (string, []byte) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		require.NoError(t, w.SetBoundary(boundary))
		require.NoError(t, w.WriteField("model", "test"))
		if duplicate {
			require.NoError(t, w.WriteField("model", "test"))
		}
		f, e := w.CreateFormFile("file", "sample.pdf")
		require.NoError(t, e)
		_, e = f.Write([]byte("file bytes"))
		require.NoError(t, e)
		require.NoError(t, w.Close())
		return w.FormDataContentType(), buf.Bytes()
	}
	ct, b := makeBody("aaa", false)
	r, e := parseOperation(Config{}, "ocr", ct, b)
	require.NoError(t, e)
	ct, b = makeBody("bbb", false)
	other, e := parseOperation(Config{}, "ocr", ct, b)
	require.NoError(t, e)
	assert.Equal(t, r.Canonical, other.Canonical)
	ct, b = makeBody("ccc", true)
	_, e = parseOperation(Config{}, "ocr", ct, b)
	require.Error(t, e)
}

func TestConcurrentMeteredReceiptsPersistConflict(t *testing.T) {
	a, db, _ := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	// Independent sessions model independent provider callbacks, not a shared lock.
	other := &Session{adapter: a, call: s.call}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, session := range []*Session{s, other} {
		go func(n int, x *Session) { <-start; results <- x.ObserveMetering(ctx, speechUsage(strconv.Itoa(n+1))) }(i, session)
	}
	close(start)
	first, second := <-results, <-results
	require.True(t, (first == nil && errors.Is(second, ErrConflict)) || (second == nil && errors.Is(first, ErrConflict)), "%v / %v", first, second)
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.True(t, row.EvidenceConflict)
	assert.Equal(t, "UNKNOWN", row.Status)
	require.ErrorIs(t, s.Finish(ctx), ErrConflict)
}
func TestTaskBindingAndBusinessInsertAreAtomic(t *testing.T) {
	a, db, _ := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	failure := errors.New("injected task insert failure")
	require.ErrorIs(t, s.BindTaskTransaction(ctx, "provider", func(*gorm.DB) error { return failure }), failure)
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Empty(t, row.ProviderTaskID)
	assert.Equal(t, "UNKNOWN", row.Status)
	require.NoError(t, s.BindTaskTransaction(ctx, "provider", func(tx *gorm.DB) error {
		return tx.Model(&Attempt{}).Where("call_id = ?", "call").Update("status", "TASK_STORED").Error
	}))
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "provider", row.ProviderTaskID)
}
func TestRecoverDurableProviderBindingBeforeObservation(t *testing.T) {
	a, _, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	// Worker reconstructs the exact provider ID from its durable OCR submission.
	require.NoError(t, a.BindTask(ctx, "call", "durable-provider"))
	require.NoError(t, a.BindTask(ctx, "call", "durable-provider"))
	require.Error(t, a.BindTask(ctx, "call", "wrong-provider"))
	require.NoError(t, a.ObserveTask(ctx, "call", "durable-provider", speechUsage("1")))
	assert.EqualValues(t, 1, settles.Load())
}
