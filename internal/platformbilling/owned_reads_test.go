package platformbilling

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOwnedTaskReadBindsOriginalCallAndDistinctAuthority(t *testing.T) {
	row := Call{CallID: "owned", Operation: "ocr", WireOperation: "ocr.jobs.create", ProviderTaskID: "native-id", CredentialKind: "task", CredentialID: "mc-original", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "ocr", Digest: strings.Repeat("a", 64), Status: "UNKNOWN"}
	claims := OwnedReadClaims{CredentialKind: "owned_call_read_v1", Audience: "llmrouter-read", ClientID: "sw", CallID: row.CallID, RequestDigest: row.Digest, Model: row.Model, WireOperation: row.WireOperation, TaskID: row.TaskID, CredentialID: row.CredentialID, OriginalCredentialKind: row.CredentialKind, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, ExpiresAt: time.Now().Add(4 * time.Minute)}
	current := claims
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "/internal/v1/call-reads/verify", r.URL.Path)
		assert.Equal(t, "Bearer "+strings.Repeat("s", 32), r.Header.Get("Authorization"))
		b, _ := common.Marshal(current)
		_, _ = w.Write(b)
	}))
	defer server.Close()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/reads.db"), &gorm.Config{})
	require.NoError(t, err)
	cfg := Config{Enabled: true, Origin: server.URL, ServiceToken: strings.Repeat("s", 32), Group: "default", MaxBodyBytes: 1024, MaxInputTokens: 100, MaxOutputTokens: 100}
	a, err := New(db, cfg)
	require.NoError(t, err)
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, db.Create(&Attempt{CallID: row.CallID, ChannelID: 2, Status: "DISPATCHING"}).Error)
	token := "uh_read_" + strings.Repeat("b", 64)
	got, err := a.OwnedTaskRead(context.Background(), token, "owned")
	require.NoError(t, err)
	assert.Equal(t, row.CallID, got.CallID)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("credential_kind", nil).Error)
	a, err = New(db, cfg)
	require.NoError(t, err)
	_, err = a.OwnedTaskRead(context.Background(), token, "owned")
	require.NoError(t, err)
	cases := map[string]func(*OwnedReadClaims){"actor": func(c *OwnedReadClaims) { c.ActorID = "other" }, "account": func(c *OwnedReadClaims) { c.AccountID = "other" }, "product": func(c *OwnedReadClaims) { c.OriginProductID = "other" }, "instance": func(c *OwnedReadClaims) { c.InstanceID = "other" }, "key": func(c *OwnedReadClaims) { c.CredentialID = "other" }, "kind": func(c *OwnedReadClaims) { c.CredentialKind = "model" }, "audience": func(c *OwnedReadClaims) { c.Audience = "llmrouter" }, "model": func(c *OwnedReadClaims) { c.Model = "other" }, "wire": func(c *OwnedReadClaims) { c.WireOperation = "videos.create" }, "digest": func(c *OwnedReadClaims) { c.RequestDigest = "other" }, "task": func(c *OwnedReadClaims) { c.TaskID = "other" }, "expiry": func(c *OwnedReadClaims) { c.ExpiresAt = time.Now().Add(-time.Minute) }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			current = claims
			mutate(&current)
			_, err := a.OwnedTaskRead(context.Background(), token, "owned")
			require.Error(t, err)
		})
	}
	before := requests
	_, err = a.OwnedTaskRead(context.Background(), "uh_model_"+strings.Repeat("b", 64), "owned")
	require.Error(t, err)
	assert.Equal(t, before, requests)
	_, err = a.OwnedTaskRead(context.Background(), token, "native-id")
	require.Error(t, err)
	assert.Equal(t, before, requests)
	assert.False(t, IsModelCredential(token))
}

func TestOwnedTaskReadIncludesPlatformVideoJobs(t *testing.T) {
	row := Call{CallID: "owned-video", Operation: "video", WireOperation: "videos.create", ProviderTaskID: "provider-video", CredentialKind: "api_key", CredentialID: "key-original", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "wan2.6-t2v", Digest: strings.Repeat("b", 64), Status: "UNKNOWN"}
	claims := OwnedReadClaims{CredentialKind: "owned_call_read_v1", Audience: "llmrouter-read", ClientID: "sw", CallID: row.CallID, RequestDigest: row.Digest, Model: row.Model, WireOperation: row.WireOperation, TaskID: row.TaskID, CredentialID: row.CredentialID, OriginalCredentialKind: row.CredentialKind, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, ExpiresAt: time.Now().Add(4 * time.Minute)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/call-reads/verify", r.URL.Path)
		data, _ := common.Marshal(claims)
		_, _ = w.Write(data)
	}))
	defer server.Close()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/video-reads.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: server.URL, ServiceToken: strings.Repeat("s", 32), Group: "default", MaxBodyBytes: 1024, MaxInputTokens: 100, MaxOutputTokens: 100})
	require.NoError(t, err)
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, db.Create(&Attempt{CallID: row.CallID, ChannelID: 16, Status: "DISPATCHED"}).Error)
	got, err := a.OwnedTaskRead(context.Background(), "uh_read_"+strings.Repeat("b", 64), row.CallID)
	require.NoError(t, err)
	assert.Equal(t, row.CallID, got.CallID)
	assert.Equal(t, "videos.create", got.WireOperation)
}
