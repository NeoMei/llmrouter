package accountpool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func relayPoolFixture(t *testing.T) (*gin.Context, *gorm.DB, []model.UpstreamAccountRef) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("channel_type", constant.ChannelTypeOpenAI)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	refs := []model.UpstreamAccountRef{}
	for index, key := range []string{"appA+tokenA", "appB+tokenB"} {
		ref, err := model.ResolveUpstreamAccountRef(c.Request.Context(), db, "channel:11", 11, index, key, "")
		require.NoError(t, err)
		refs = append(refs, ref)
	}
	scope := RequestScope(c, "m")
	selected, err := model.SelectUpstreamAccount(c.Request.Context(), db, refs, scope, nil, time.Now())
	require.NoError(t, err)
	Prepare(c, db, selected, scope)
	State(c).Candidates = refs
	return c, db, refs
}

func TestRelayAccountOutcomeUsesSentReceiptAndRestoresBody(t *testing.T) {
	c, db, refs := relayPoolFixture(t)
	require.NoError(t, BeginSend(c))
	sent := *State(c).Sent
	// Selection can change, but attribution must use the sent value.
	State(c).Selected = &model.UpstreamAccountSelection{Ref: refs[1]}
	original := `{"error":{"code":"invalid_api_key","message":"secret must not be persisted"}}`
	response := &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(original))}
	require.NoError(t, Observe(c, response, nil))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, original, string(body))
	selected, err := model.SelectUpstreamAccount(context.Background(), db, refs, State(c).Scope, nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, refs[1].MemberID, selected.Ref.MemberID)
	assert.Equal(t, refs[0].MemberID, sent.Ref.MemberID)
	var states []model.UpstreamAccountState
	require.NoError(t, db.Find(&states).Error)
	require.Len(t, states, 1)
	assert.Equal(t, "invalid_credential", states[0].Reason)
}

func TestRelayAccountGeneric403NeverInvalidates(t *testing.T) {
	c, db, refs := relayPoolFixture(t)
	require.NoError(t, BeginSend(c))
	require.NoError(t, Observe(c, &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized quota billing"}}`))}, nil))
	_, err := model.SelectUpstreamAccount(context.Background(), db, refs[:1], State(c).Scope, nil, time.Now())
	require.NoError(t, err)
}

func TestRelayAccountRetryGuards(t *testing.T) {
	for _, test := range []struct {
		name, path           string
		status               int
		veto, output, cancel bool
		want                 bool
	}{
		{name: "safe-rate-limit", path: "/v1/chat/completions", status: 429, want: true},
		{name: "unsafe-explicit-refusal", path: "/v1/images/generations", status: 429, want: true},
		{name: "unsafe-unknown", path: "/v1/images/generations", status: 502},
		{name: "upstream-veto", path: "/v1/chat/completions", status: 429, veto: true},
		{name: "output", path: "/v1/chat/completions", status: 429, output: true},
		{name: "cancel", path: "/v1/chat/completions", status: 429, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _, _ := relayPoolFixture(t)
			c.Request.URL.Path = test.path
			require.NoError(t, BeginSend(c))
			response := &http.Response{StatusCode: test.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}
			if test.veto {
				response.Header.Set("X-Should-Retry", "false")
			}
			require.NoError(t, Observe(c, response, nil))
			if test.output {
				_, _ = c.Writer.Write([]byte("data"))
			}
			if test.cancel {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			assert.Equal(t, test.want, RetryAllowed(c))
		})
	}
}

func TestRelayAccountNoMemberRepeatAndAllCooledRetryAfter(t *testing.T) {
	c, db, refs := relayPoolFixture(t)
	for range 2 {
		require.NoError(t, BeginSend(c))
		require.NoError(t, Observe(c, &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil))
		ready, err := PrepareNext(c)
		require.NoError(t, err)
		if len(State(c).Excluded) == 1 {
			assert.True(t, ready)
		} else {
			assert.False(t, ready)
		}
	}
	require.Error(t, BeginSend(c))
	_, err := model.SelectUpstreamAccount(context.Background(), db, refs, State(c).Scope, nil, time.Now())
	var unavailable *model.UpstreamAccountUnavailable
	require.ErrorAs(t, err, &unavailable)
	assert.WithinDuration(t, time.Now().Add(120*time.Second), unavailable.RetryAt, time.Second)
	fresh, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetUnavailableHeader(fresh, err)
	assert.Equal(t, "120", fresh.Writer.Header().Get("Retry-After"))
}

func TestRelayAccountRandomSelectionExcludesEverySentMember(t *testing.T) {
	c, _, refs := relayPoolFixture(t)
	State(c).Random = true
	require.NoError(t, BeginSend(c))
	ready, err := PrepareNext(c)
	require.NoError(t, err)
	require.True(t, ready)
	require.NoError(t, BeginSend(c))
	assert.NotEqual(t, refs[0].MemberID, State(c).Sent.Ref.MemberID)
	ready, err = PrepareNext(c)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestRelayAccountUnsafeNetworkAndActualSendBudget(t *testing.T) {
	c, _, _ := relayPoolFixture(t)
	c.Request.URL.Path = "/v1/videos"
	State(c).MaxSends = 1
	require.NoError(t, BeginSend(c))
	require.NoError(t, Observe(c, nil, context.DeadlineExceeded))
	assert.True(t, State(c).Uncertain)
	assert.False(t, RetryAllowed(c))
	ready, err := PrepareNext(c)
	require.NoError(t, err)
	require.True(t, ready)
	require.Error(t, BeginSend(c))
	assert.Equal(t, 1, State(c).Sends)
}

func TestRelayAccountCredentialClassificationRequiresKnownProtocol(t *testing.T) {
	for _, test := range []struct {
		name, code string
		channel    int
	}{
		{name: "resource-token", code: "invalid_token", channel: constant.ChannelTypeOpenAI},
		{name: "unknown-protocol", code: "invalid_api_key", channel: constant.ChannelTypeCustom},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, db, refs := relayPoolFixture(t)
			c.Set("channel_type", test.channel)
			require.NoError(t, BeginSend(c))
			response := &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + test.code + `"}}`))}
			require.NoError(t, Observe(c, response, nil))
			_, err := model.SelectUpstreamAccount(context.Background(), db, refs[:1], State(c).Scope, nil, time.Now())
			require.NoError(t, err)
		})
	}
}

func TestPooledResourcesAllowOnlyPreparedTextHistory(t *testing.T) {
	for _, test := range []struct {
		name      string
		prepared  *dto.OpenAIResponsesRequest
		wantError bool
	}{
		{name: "opaque-parent", wantError: true},
		{name: "scoped-text-history", prepared: &dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"message","role":"user","content":"hello"}]`)}},
		{name: "scoped-file-still-owned", prepared: &dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"input_file","file_id":"file-A"}]`)}, wantError: true},
		{name: "scoped-ciphertext-still-owned", prepared: &dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"reasoning","encrypted_content":"cipher"}]`)}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _, _ := relayPoolFixture(t)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","previous_response_id":"resp-A","input":"next"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			if test.prepared != nil {
				c.Set(string(constant.ContextKeyResponsesPreparedRequest), test.prepared)
			}
			err := ValidateResources(c)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
