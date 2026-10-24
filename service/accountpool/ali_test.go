package accountpool

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The compatible transport observer and native semantic parser must share the
// same provider-specific diagnosis while keeping their single outcome owners.
func TestAliHTTPFundingVersusTPM(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, tc := range []struct {
			name, body    string
			status        int
			funding, rate bool
		}{
			{"compatible-tpm", `{"error":{"code":"insufficient_quota"}}`, 429, false, true},
			{"native-tpm", `{"code":"Throttling.AllocationQuota"}`, 429, false, true},
			{"compatible-funding", `{"error":{"code":"Arrearage"}}`, 400, true, false},
			{"native-funding", `{"code":"Arrearage"}`, 400, true, false},
			{"voice-storage", `{"code":"Throttling.AllocationQuota"}`, 400, false, false},
			{"ambiguous403", `{"code":"Forbidden","message":"insufficient balance this month"}`, 403, false, false},
		} {
			mode := "compatible-observe"
			if native {
				mode = "native-complete"
			}
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				c, db, _ := relayPoolFixture(t)
				c.Set("channel_type", constant.ChannelTypeAli)
				refs := []model.UpstreamAccountRef{}
				for i, key := range []string{"ali-A", "ali-B"} {
					ref, err := model.ResolveUpstreamAccountRef(c.Request.Context(), db, "ali-pool", 15, i, key, "shared-funds")
					require.NoError(t, err)
					refs = append(refs, ref)
				}
				scope := model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "mapped-qwen"}
				selected, err := model.SelectUpstreamAccount(c.Request.Context(), db, refs[:1], scope, nil, time.Now())
				require.NoError(t, err)
				Prepare(c, db, selected, scope)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "Bearer ali-A", r.Header.Get("Authorization"))
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				}))
				defer server.Close()
				request, err := http.NewRequestWithContext(c.Request.Context(), "POST", server.URL, strings.NewReader(`{"model":"mapped-qwen"}`))
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer ali-A")
				if native {
					DeferAllStatusNativeOutcome(c)
				}
				require.NoError(t, BeginSend(c))
				response, err := server.Client().Do(request)
				require.NoError(t, err)
				defer response.Body.Close()
				require.NoError(t, Observe(c, response, nil))
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(raw))
				State(c).Selected = &model.UpstreamAccountSelection{Ref: refs[1]}
				if native {
					failure, _ := AliFailure(response.StatusCode, "", raw)
					require.NoError(t, CompleteNativeOutcome(c, &failure))
					require.Error(t, CompleteNativeOutcome(c, nil))
				}
				_, err = model.SelectUpstreamAccount(c.Request.Context(), db, refs[1:], scope, nil, time.Now())
				if tc.funding {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				other := model.UpstreamAccountScope{Operation: "images.generations.create", Model: "other-model"}
				_, err = model.SelectUpstreamAccount(c.Request.Context(), db, refs[:1], other, nil, time.Now())
				if tc.funding {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				var states []model.UpstreamAccountState
				require.NoError(t, db.Find(&states).Error)
				if tc.funding || tc.rate {
					require.Len(t, states, 1)
					if tc.funding {
						assert.Equal(t, "quota:shared-funds", states[0].Subject)
						assert.Empty(t, states[0].Operation)
					} else {
						assert.Equal(t, "member:"+refs[0].MemberID, states[0].Subject)
						assert.Equal(t, scope.Operation, states[0].Operation)
						assert.Equal(t, scope.Model, states[0].Model)
					}
				} else {
					assert.Empty(t, states)
				}
			})
		}
	}
}
