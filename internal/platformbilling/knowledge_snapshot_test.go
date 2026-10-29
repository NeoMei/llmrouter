package platformbilling

import (
	"bytes"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAliKnowledgeSnapshotSealsInputAndBindsItsOwner(t *testing.T) {
	db := knowledgeResultTestDB(t, "sqlite", "file:"+t.TempDir()+"/input.db")
	keys := map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32), "v2": bytes.Repeat([]byte{2}, 32)}
	v, err := newKnowledgeResultVaultWithKeys(db, keys, "v1", time.Hour)
	require.NoError(t, err)
	a := &Adapter{knowledgeResults: v}
	policy := KnowledgePolicy{Version: "knowledge_ali_text_v1", PublicModel: "model", ChannelID: 1}
	plain, err := common.Marshal(frozenKnowledge{Policy: policy, Body: []byte(`{"input":["sensitive data"]}`)})
	require.NoError(t, err)
	row := Call{CallID: "call", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "model", Digest: "digest", WireOperation: "embeddings.create", MediaSnapshot: string(plain)}
	require.NoError(t, a.sealKnowledgeSnapshot(&row))
	assert.NotContains(t, row.MediaSnapshot, "sensitive data")
	got, err := a.knowledgeSnapshot(row)
	require.NoError(t, err)
	assert.JSONEq(t, `{"input":["sensitive data"]}`, string(got.Body))
	// Restart retains all key versions, including the original ciphertext key.
	reopened, err := newKnowledgeResultVaultWithKeys(db, keys, "v1", time.Hour)
	require.NoError(t, err)
	a.knowledgeResults = reopened
	_, err = a.knowledgeSnapshot(row)
	require.NoError(t, err)
	for _, mutate := range []func(*Call){
		func(c *Call) { c.ActorID = "other" },
		func(c *Call) { c.AccountID = "other" },
		func(c *Call) { c.TaskID = "other" },
		func(c *Call) { c.CallID = "other" },
		func(c *Call) { c.Digest = "other" },
		func(c *Call) { c.MediaSnapshot = string(plain) },
	} {
		changed := row
		mutate(&changed)
		_, err := a.knowledgeSnapshot(changed)
		assert.Error(t, err)
	}
	var f frozenKnowledge
	require.NoError(t, common.UnmarshalUnique([]byte(row.MediaSnapshot), &f))
	f.BodyCiphertext[len(f.BodyCiphertext)-1] ^= 1
	bad, err := common.Marshal(f)
	require.NoError(t, err)
	row.MediaSnapshot = string(bad)
	_, err = a.knowledgeSnapshot(row)
	assert.Error(t, err)
}
