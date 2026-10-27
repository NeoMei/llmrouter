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

func TestManagedTaskReadRequiresSameVerifiedKeyAndScope(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/model-credentials/verify", r.URL.Path)
		var in map[string]string
		require.NoError(t, common.DecodeJson(r.Body, &in))
		assert.Equal(t, "receipt", in["purpose"])
		assert.Equal(t, "instance-a", in["instance_id"])
		keyID, actor, payer, origin := "key-id", "actor", "payer", "client"
		expires := time.Now().Add(time.Hour)
		switch in["token"] {
		case "uh_key_otherkey":
			keyID = "other"
		case "uh_key_otheractor":
			actor = "other"
		case "uh_key_otherpayer":
			payer = "other"
		case "uh_key_otherorigin":
			origin = "other"
		case "uh_key_expired":
			expires = time.Now().Add(-time.Hour)
		case "uh_key_revoked":
			w.WriteHeader(403)
			return
		}
		out := Claims{ID: keyID, CredentialKind: "api_key", InstanceID: "instance-a", Audience: "llmrouter", ActorID: actor, AccountID: payer, OriginProductID: origin, Models: []string{"test"}, ExpiresAt: expires}
		data, err := common.Marshal(out)
		require.NoError(t, err)
		_, _ = w.Write(data)
	}))
	defer hub.Close()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/tasks.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "instance-a", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	row := Call{CallID: "call", CredentialID: "key-id", ActorID: "actor", AccountID: "payer", OriginProductID: "client", InstanceID: "instance-a", TaskID: "server-task", Model: "test", Operation: "video", ProviderTaskID: "provider-task", Status: "UNKNOWN"}
	require.NoError(t, db.Create(&row).Error)
	for _, id := range []string{"call", "provider-task"} {
		got, err := a.ManagedTask(context.Background(), "uh_key_original", id)
		require.NoError(t, err)
		assert.Equal(t, "server-task", got.TaskID)
		assert.Equal(t, "call", got.CallID)
	}
	for _, key := range []string{"uh_key_otherkey", "uh_key_otheractor", "uh_key_otherpayer", "uh_key_otherorigin"} {
		_, err := a.ManagedTask(context.Background(), key, "call")
		require.ErrorIs(t, err, ErrInvalid)
	}
	_, err = a.Receipt(context.Background(), "uh_key_otherkey", "server-task", "call")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = a.ManagedTask(context.Background(), "uh_key_expired", "call")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = a.ManagedTask(context.Background(), "uh_key_revoked", "call")
	require.Error(t, err)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Update("instance_id", "instance-b").Error)
	_, err = a.ManagedTask(context.Background(), "uh_key_original", "call")
	require.ErrorIs(t, err, ErrInvalid)
}
