package platformbilling

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeResultKeyRotation(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/rotation.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&Call{}))
			old := []byte("0123456789abcdef0123456789abcdef")
			next := []byte("abcdef0123456789abcdef0123456789")
			v1, err := newKnowledgeResultVault(db, old, time.Hour)
			require.NoError(t, err)
			mk := func(id string) Call {
				return Call{CallID: id, InstanceID: "default", ActorID: "actor", AccountID: "payer", TaskID: "task", OriginProductID: "superwagie", Model: "model", Operation: "embedding", WireOperation: "embeddings.create", Digest: "digest", Status: "UNKNOWN", Evidence: `{"version":"metered_v2","operation":"embedding","state":"confirmed"}`}
			}
			row := mk("old-call")
			require.NoError(t, db.Create(&row).Error)
			require.NoError(t, v1.capture(context.Background(), row.CallID, []byte("old output")))
			// Rows made by the old binary have no key ID and must continue
			// to use the explicit legacy v1 key after rotation.
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("key_id", "").Error)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "SETTLED").Error)
			keys := map[string][]byte{"v1": old, "v2": next}
			staged, err := newKnowledgeResultVaultWithKeys(db, keys, "v1", time.Hour)
			require.NoError(t, err)
			require.NoError(t, staged.activateKey(context.Background(), "v2"))
			_, err = newKnowledgeResultVaultWithKeys(db, keys, "v1", time.Hour)
			assert.Error(t, err)
			rotated, err := newKnowledgeResultVaultWithKeys(db, keys, "v2", time.Hour)
			require.NoError(t, err)
			got, err := rotated.readSettled(context.Background(), row.CallID)
			require.NoError(t, err)
			assert.Equal(t, []byte("old output"), got)
			row2 := mk("new-call")
			require.NoError(t, db.Create(&row2).Error)
			assert.Error(t, staged.capture(context.Background(), row2.CallID, []byte("new output")))
			require.NoError(t, rotated.capture(context.Background(), row2.CallID, []byte("new output")))
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row2.CallID).Update("status", "SETTLED").Error)
			got, err = rotated.readSettled(context.Background(), row2.CallID)
			require.NoError(t, err)
			assert.Equal(t, []byte("new output"), got)
			_, err = newKnowledgeResultVaultWithKeys(db, map[string][]byte{"v2": next}, "v2", time.Hour)
			assert.Error(t, err)
			_, err = newKnowledgeResultVaultWithKeys(db, map[string][]byte{"v1": old, "v2": old}, "v2", time.Hour)
			assert.Error(t, err)
		})
	}
}

func TestKnowledgeResultKeyRotationPreservesBoundV2(t *testing.T) {
	db := knowledgeResultTestDB(t, "sqlite", "file:"+t.TempDir()+"/bound.db")
	s, key, upstream := responseFixtureOnDB(t, "rerank", db)
	old := []byte("0123456789abcdef0123456789abcdef")
	next := []byte("abcdef0123456789abcdef0123456789")
	v, err := newKnowledgeResultVault(db, old, time.Hour)
	require.NoError(t, err)
	raw := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
	staged, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))})
	require.NoError(t, err)
	require.NoError(t, v.captureValidated(context.Background(), s, staged))
	require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", s.call.CallID).Update("key_id", "").Error)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("status", "SETTLED").Error)
	ring := map[string][]byte{"v1": old, "v2": next}
	stagedVault, err := newKnowledgeResultVaultWithKeys(db, ring, "v1", time.Hour)
	require.NoError(t, err)
	require.NoError(t, stagedVault.activateKey(context.Background(), "v2"))
	rotated, err := newKnowledgeResultVaultWithKeys(db, ring, "v2", time.Hour)
	require.NoError(t, err)
	got, err := rotated.readSettled(context.Background(), s.call.CallID)
	require.NoError(t, err)
	assert.Equal(t, staged.body, string(got))
}

func TestKnowledgeResultKeyConcurrentConflict(t *testing.T) {
	db := knowledgeResultTestDB(t, "sqlite", "file:"+t.TempDir()+"/conflict.db?_pragma=busy_timeout(5000)")
	require.NoError(t, db.AutoMigrate(&Call{}))
	old := []byte("0123456789abcdef0123456789abcdef")
	_, err := newKnowledgeResultVault(db, old, time.Hour)
	require.NoError(t, err)
	first := []byte("abcdef0123456789abcdef0123456789")
	other := []byte("fedcba9876543210fedcba9876543210")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range [][]byte{first, other} {
		wg.Add(1)
		go func(key []byte) {
			defer wg.Done()
			<-start
			_, err := newKnowledgeResultVaultWithKeys(db, map[string][]byte{"v1": old, "v2": key}, "v1", time.Hour)
			results <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	assert.Equal(t, 1, success)
}

func TestKnowledgeResultRotationSerializesCapture(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/serial.db?_pragma=busy_timeout(5000)"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&Call{}))
			old := []byte("0123456789abcdef0123456789abcdef")
			next := []byte("abcdef0123456789abcdef0123456789")
			ring := map[string][]byte{"v1": old, "v2": next}
			v, err := newKnowledgeResultVaultWithKeys(db, ring, "v1", time.Hour)
			require.NoError(t, err)
			row := Call{CallID: "racing-call", InstanceID: "default", ActorID: "actor", AccountID: "payer", TaskID: "task", OriginProductID: "superwagie", Model: "model", Operation: "embedding", WireOperation: "embeddings.create", Digest: "digest", Status: "UNKNOWN", Evidence: `{"version":"metered_v2","operation":"embedding","state":"confirmed"}`}
			require.NoError(t, db.Create(&row).Error)
			start := make(chan struct{})
			capture := make(chan error, 1)
			rotation := make(chan error, 1)
			go func() { <-start; capture <- v.capture(context.Background(), row.CallID, []byte("result")) }()
			go func() { <-start; rotation <- v.activateKey(context.Background(), "v2") }()
			close(start)
			captureErr, rotationErr := <-capture, <-rotation
			// SQLite can reject one writer as busy. After it finishes, transition may
			// safely be retried; no output is released on an error.
			if rotationErr != nil {
				require.NoError(t, v.activateKey(context.Background(), "v2"))
			}
			updated, err := newKnowledgeResultVaultWithKeys(db, ring, "v2", time.Hour)
			require.NoError(t, err)
			if captureErr == nil {
				require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "SETTLED").Error)
				got, err := updated.readSettled(context.Background(), row.CallID)
				require.NoError(t, err)
				assert.Equal(t, []byte("result"), got)
			} else {
				require.NoError(t, updated.capture(context.Background(), row.CallID, []byte("result")))
			}
			other := row
			other.CallID = "stale-call"
			require.NoError(t, db.Create(&other).Error)
			assert.Error(t, v.capture(context.Background(), other.CallID, []byte("stale")))
		})
	}
}

func TestKnowledgeResultRejectsMissingUsedKeyVerifier(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/damaged.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_MYSQL_DSN"); dsn != "" {
		targets["mysql"] = dsn
	}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		for _, keyID := range []string{"", "v2"} {
			t.Run(dialect+"/"+map[string]string{"": "legacy-v1", "v2": "named-v2"}[keyID], func(t *testing.T) {
				// Separate sqlite databases and postgres schemas for each damaged case.
				if dialect == "sqlite" {
					dsn = "file:" + t.TempDir() + "/damaged.db"
				}
				db := knowledgeResultTestDB(t, dialect, dsn)
				require.NoError(t, db.AutoMigrate(&Call{}))
				old := []byte("0123456789abcdef0123456789abcdef")
				next := []byte("abcdef0123456789abcdef0123456789")
				ring := map[string][]byte{"v1": old, "v2": next}
				v, err := newKnowledgeResultVaultWithKeys(db, ring, "v1", time.Hour)
				require.NoError(t, err)
				if keyID == "v2" {
					require.NoError(t, v.activateKey(context.Background(), "v2"))
					v, err = newKnowledgeResultVaultWithKeys(db, ring, "v2", time.Hour)
					require.NoError(t, err)
				}
				row := Call{CallID: "damaged-call", InstanceID: "default", ActorID: "actor", AccountID: "payer", TaskID: "task", OriginProductID: "superwagie", Model: "model", Operation: "embedding", WireOperation: "embeddings.create", Digest: "digest", Status: "UNKNOWN", Evidence: `{"version":"metered_v2","operation":"embedding","state":"confirmed"}`}
				require.NoError(t, db.Create(&row).Error)
				require.NoError(t, v.capture(context.Background(), row.CallID, []byte("existing ciphertext")))
				damagedID := keyID
				if damagedID == "" {
					damagedID = "v1"
					// Migration of an old table leaves this new column NULL.
					require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("key_id", nil).Error)
				}
				require.NoError(t, db.Where("id = ?", damagedID).Delete(&knowledgeResultKey{}).Error)
				replacement := map[string][]byte{"v1": old, "v2": next}
				replacement[damagedID] = []byte("fedcba9876543210fedcba9876543210")
				_, err = newKnowledgeResultVaultWithKeys(db, replacement, v.active, time.Hour)
				assert.Error(t, err)
				var count int64
				require.NoError(t, db.Model(&knowledgeResultKey{}).Where("id = ?", damagedID).Count(&count).Error)
				assert.Zero(t, count, "failed startup must not register a replacement verifier")
				var stored knowledgeResultRow
				require.NoError(t, db.First(&stored, "call_id = ?", row.CallID).Error)
				assert.NotEmpty(t, stored.Ciphertext)
			})
		}
	}
}
