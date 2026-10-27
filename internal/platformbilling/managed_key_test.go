package platformbilling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDurableKeyAdmissionRequiresVerifiedKindAndInstance(t *testing.T) {
	for _, tc := range []struct {
		name, kind, instance string
		expired              bool
		want                 bool
	}{
		{"durable", "api_key", "instance-a", false, true},
		{"wrong instance", "api_key", "instance-b", false, false},
		{"missing instance", "api_key", "", false, false},
		{"task masquerading as durable", "task", "instance-a", false, false},
		{"missing kind", "", "instance-a", false, false},
		{"expired", "api_key", "instance-a", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reserves int
			rate := Rate{ID: "rate", Model: "test", Status: "published", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"1", "1"}, CacheRead: Rational{"1", "1"}, CacheWrite: Rational{"1", "1"}, Reasoning: Rational{"1", "1"}}
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					var in map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &in))
					assert.Equal(t, "instance-a", in["instance_id"])
					expiry := time.Time{}
					if tc.expired {
						expiry = time.Now().Add(-time.Minute)
					}
					out = Claims{ID: "key-id", Audience: "llmrouter", ActorID: "actor", AccountID: "payer", OriginProductID: "client", Models: []string{"test"}, InstanceID: tc.instance, CredentialKind: tc.kind, ExpiresAt: expiry}
				case "/internal/v1/rates":
					out = []Rate{rate}
				case "/internal/v1/calls/reserve":
					reserves++
					var in map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &in))
					assert.Equal(t, "instance-a", in["instance_id"])
					assert.Equal(t, "server-task", in["task_id"])
					out = RemoteCall{CallID: in["call_id"], ActorID: "actor", AccountID: "payer", OriginProductID: "client", InstanceID: "instance-a", CredentialID: "key-id", RequestDigest: in["request_digest"], Rate: rate, Status: "RESERVED"}
				default:
					t.Errorf("unexpected operation %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				data, err := common.Marshal(out)
				require.NoError(t, err)
				_, _ = w.Write(data)
			}))
			defer hub.Close()
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/key.db"), &gorm.Config{})
			require.NoError(t, err)
			a, err := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "instance-a", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
			require.NoError(t, err)
			s, err := a.Admit(context.Background(), "uh_key_secret", "server-task", "server-call", []byte(`{"model":"test","messages":[],"max_tokens":20}`))
			if tc.want {
				require.NoError(t, err)
				assert.Equal(t, "instance-a", s.VerifiedScope().InstanceID)
				assert.Equal(t, 1, reserves)
			} else {
				require.Error(t, err)
				assert.Zero(t, reserves)
			}
		})
	}
}
