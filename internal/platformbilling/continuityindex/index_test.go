package continuityindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{ActiveKeyVersion: "v1", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)}, TTL: 24 * time.Hour, Capacity: 1000, Now: func() time.Time { return testTime }, ValidateCreator: func(_ context.Context, o Owner, b Binding) (Target, error) {
		if o.RootTaskID != b.RootTaskID || b.Provenance.CallID == "invalid-call" {
			return Target{}, errors.New("private creator mismatch")
		}
		return completion().Target, nil
	}}
}
func openIndex(t *testing.T, path string, c Config) (*Index, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	idx, err := New(db, c)
	require.NoError(t, err)
	require.NoError(t, idx.Migrate(context.Background()))
	return idx, db
}
func completion() TrustedCompletion {
	return TrustedCompletion{Owner: Owner{ActorID: "actor-private", AccountID: "payer-private", ProductID: "superwagie", InstanceID: "instance-a", Mode: TaskCredential, CredentialID: "task-token-original", RootTaskID: "root-a"}, Target: Target{ChannelID: 42, MemberID: "member-a", PoolID: "pool-a", Namespace: "https://provider.private/v1", APIOrganization: "org-private"}, Provenance: Provenance{CallID: "call-a", AttemptID: 7, PublicModel: "public-model", UpstreamModel: "upstream-model", Wire: "responses.create", ObservationDigest: strings.Repeat("a", 64), PolicyVersion: "policy-v1"}, Completed: true, Storage: ExplicitStored, ResponseID: "resp_秘密-AbC", OutputItemIDs: []string{"item_秘密-AbC"}, CreatedAt: testTime, ExpiresAt: testTime.Add(time.Hour)}
}
func selectors(c TrustedCompletion) []Selector {
	return []Selector{{Response, c.ResponseID}, {OutputItem, c.OutputItemIDs[0]}}
}

// Losing durable ownership, refreshing a task token incorrectly, or storing raw
// provider identifiers must break this test.
func TestPersistentOwnershipAndPrivacy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	c := completion()
	idx, db := openIndex(t, path, testConfig())
	require.NoError(t, idx.Register(ctx, c))
	require.NoError(t, idx.Register(ctx, c))
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	idx, _ = openIndex(t, path, testConfig())
	o := c.Owner
	o.CredentialID = "fresh-task-token"
	got, err := idx.Resolve(ctx, o, selectors(c))
	require.NoError(t, err)
	assert.Equal(t, "root-a", got.RootTaskID)
	require.Len(t, got.Bindings, 2)
	assert.Equal(t, "call-a", got.Bindings[0].Provenance.CallID)
	assert.Equal(t, uint64(7), got.Bindings[0].Provenance.AttemptID)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, secret := range []string{c.ResponseID, c.OutputItemIDs[0], c.Owner.ActorID, c.Owner.AccountID, c.Owner.CredentialID, c.Target.Namespace, c.Target.APIOrganization} {
		assert.NotContains(t, string(raw), secret)
	}
}
func TestScopeAndSelectorIsolation(t *testing.T) {
	ctx := context.Background()
	c := completion()
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	require.NoError(t, idx.Register(ctx, c))
	for name, change := range map[string]func(*Owner){"actor": func(o *Owner) { o.ActorID = "other" }, "account": func(o *Owner) { o.AccountID = "other" }, "product": func(o *Owner) { o.ProductID = "other" }, "instance": func(o *Owner) { o.InstanceID = "other" }, "root": func(o *Owner) { o.RootTaskID = "other" }, "key-mode": func(o *Owner) { o.Mode = DurableKey }} {
		t.Run(name, func(t *testing.T) {
			o := c.Owner
			change(&o)
			_, err := idx.Resolve(ctx, o, selectors(c))
			assert.ErrorIs(t, err, ErrUnavailable)
		})
	}
	for _, s := range []Selector{{OutputItem, c.ResponseID}, {Response, "resp_秘密-abc"}, {Response, "unknown"}, {ResourceKind("compact"), c.ResponseID}} {
		_, err := idx.Resolve(ctx, c.Owner, []Selector{s})
		assert.ErrorIs(t, err, ErrUnavailable)
	}
}
func TestExactKeyDerivesRootAndRejectsMixedRoots(t *testing.T) {
	ctx := context.Background()
	c := completion()
	c.Owner.Mode = DurableKey
	c.Owner.CredentialID = "key-a"
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	require.NoError(t, idx.Register(ctx, c))
	o := c.Owner
	o.RootTaskID = "caller-root-must-not-authorize"
	got, err := idx.Resolve(ctx, o, selectors(c))
	require.NoError(t, err)
	assert.Equal(t, "root-a", got.RootTaskID)
	o.CredentialID = "key-b"
	_, err = idx.Resolve(ctx, o, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
	other := c
	other.Owner.RootTaskID = "root-b"
	other.ResponseID = "resp-b"
	other.OutputItemIDs = nil
	other.Provenance.CallID = "call-b"
	require.NoError(t, idx.Register(ctx, other))
	o = c.Owner
	o.RootTaskID = ""
	_, err = idx.Resolve(ctx, o, []Selector{{Response, c.ResponseID}, {Response, other.ResponseID}})
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestImmutableAtomicBatchAndAmbiguousNamespace(t *testing.T) {
	ctx := context.Background()
	c := completion()
	cfg := testConfig()
	cfg.ValidateCreator = func(_ context.Context, _ Owner, b Binding) (Target, error) {
		target := completion().Target
		if b.Provenance.CallID == "call-another" {
			target.Namespace = "https://another.private/v1"
		}
		return target, nil
	}
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	require.NoError(t, idx.Register(ctx, c))
	bad := c
	bad.ResponseID = "new-response"
	bad.OutputItemIDs = []string{"new-item", c.OutputItemIDs[0]}
	bad.Owner.ActorID = "other"
	bad.Provenance.CallID = "call-other"
	require.ErrorIs(t, idx.Register(ctx, bad), ErrConflict)
	_, err := idx.Resolve(ctx, bad.Owner, []Selector{{Response, bad.ResponseID}})
	assert.ErrorIs(t, err, ErrUnavailable)
	_, err = idx.Resolve(ctx, bad.Owner, []Selector{{OutputItem, "new-item"}})
	assert.ErrorIs(t, err, ErrUnavailable)
	changed := c
	changed.ExpiresAt = changed.ExpiresAt.Add(time.Minute)
	assert.ErrorIs(t, idx.Register(ctx, changed), ErrConflict)
	other := c
	other.Target.Namespace = "https://another.private/v1"
	other.Provenance.CallID = "call-another"
	require.NoError(t, idx.Register(ctx, other))
	_, err = idx.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestExpiryCleanupAndCapacity(t *testing.T) {
	ctx := context.Background()
	clock := testTime
	cfg := testConfig()
	cfg.Now = func() time.Time { return clock }
	cfg.Capacity = 2
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	require.NoError(t, idx.Register(ctx, c))
	next := c
	next.ResponseID = "resp-next"
	next.OutputItemIDs = nil
	next.Provenance.CallID = "call-next"
	assert.ErrorIs(t, idx.Register(ctx, next), ErrCapacity)
	clock = c.ExpiresAt
	_, err := idx.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
	n, err := idx.Cleanup(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = idx.Cleanup(ctx, 500)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	next.CreatedAt = clock
	next.ExpiresAt = clock.Add(time.Hour)
	require.NoError(t, idx.Register(ctx, next))
}
func TestCompletionAndStrictBounds(t *testing.T) {
	ctx := context.Background()
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	for name, change := range map[string]func(*TrustedCompletion){
		"incomplete": func(c *TrustedCompletion) { c.Completed = false }, "compact": func(c *TrustedCompletion) { c.Provenance.Wire = "responses.compact" }, "no-store": func(c *TrustedCompletion) { c.Storage = "false" }, "no-digest": func(c *TrustedCompletion) { c.Provenance.ObservationDigest = "" }, "creator": func(c *TrustedCompletion) { c.Provenance.CallID = "invalid-call" }, "future": func(c *TrustedCompletion) { c.CreatedAt = testTime.Add(time.Second) }, "expired": func(c *TrustedCompletion) { c.ExpiresAt = testTime }, "ttl": func(c *TrustedCompletion) { c.ExpiresAt = testTime.Add(25 * time.Hour) }, "oversize-id": func(c *TrustedCompletion) { c.ResponseID = strings.Repeat("a", 256) }, "invalid-utf8": func(c *TrustedCompletion) { c.ResponseID = string([]byte{0xff}) }, "control": func(c *TrustedCompletion) { c.ResponseID = "bad\nID" }, "many-items": func(c *TrustedCompletion) { c.OutputItemIDs = make([]string, 129) }, "duplicate-items": func(c *TrustedCompletion) { c.OutputItemIDs = []string{"same", "same"} }, "empty-owner": func(c *TrustedCompletion) { c.Owner.ActorID = "" }, "large-model": func(c *TrustedCompletion) { c.Provenance.PublicModel = strings.Repeat("a", 256) }, "namespace": func(c *TrustedCompletion) { c.Target.Namespace = strings.Repeat("a", 2049) }, "no-root": func(c *TrustedCompletion) { c.Owner.RootTaskID = "" }, "no-attempt": func(c *TrustedCompletion) { c.Provenance.AttemptID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := completion()
			change(&c)
			err := idx.Register(ctx, c)
			assert.Error(t, err)
			if err != nil {
				assert.NotContains(t, err.Error(), c.ResponseID)
			}
			_, err = idx.Resolve(ctx, c.Owner, []Selector{{Response, c.ResponseID}})
			assert.ErrorIs(t, err, ErrUnavailable)
		})
	}
	_, err := idx.Resolve(ctx, completion().Owner, make([]Selector, 65))
	assert.ErrorIs(t, err, ErrUnavailable)
	for _, limit := range []int{0, 501} {
		_, err := idx.Cleanup(ctx, limit)
		assert.ErrorIs(t, err, ErrInvalid)
	}
}
func TestCreatorRevalidatedAndErrorsRedacted(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	valid := true
	cfg.ValidateCreator = func(context.Context, Owner, Binding) (Target, error) {
		if !valid {
			return Target{}, errors.New("SECRET PROVIDER ERROR")
		}
		return completion().Target, nil
	}
	idx, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	valid = false
	_, err := idx.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.NotContains(t, err.Error(), "SECRET")
	sql, e := db.DB()
	require.NoError(t, e)
	require.NoError(t, sql.Close())
	_, err = idx.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestPrivateKeyVersionsAndRotation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	cfg := testConfig()
	idx, db := openIndex(t, path, cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	cfg.Keys["v2"] = bytes.Repeat([]byte{9}, 32)
	cfg.ActiveKeyVersion = "v2"
	rotated, err := New(db, cfg)
	require.NoError(t, err)
	require.NoError(t, rotated.Migrate(ctx))
	require.NoError(t, rotated.Register(ctx, c))
	got, err := rotated.Resolve(ctx, c.Owner, selectors(c))
	require.NoError(t, err)
	assert.Equal(t, "v1", got.Bindings[0].KeyVersion)
	conflict := c
	conflict.Owner.ActorID = "other"
	assert.ErrorIs(t, rotated.Register(ctx, conflict), ErrConflict)
	delete(cfg.Keys, "v1")
	missing, err := New(db, cfg)
	require.NoError(t, err)
	require.NoError(t, missing.Migrate(ctx))
	_, err = missing.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
	conflict.ResponseID = "new"
	conflict.OutputItemIDs = nil
	assert.ErrorIs(t, missing.Register(ctx, conflict), ErrStorage)
	cfg.Keys["v1"] = bytes.Repeat([]byte{3}, 32)
	changed, err := New(db, cfg)
	require.NoError(t, err)
	assert.ErrorIs(t, changed.Migrate(ctx), ErrConfig)
}
func TestConfigurationFailsClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	for name, change := range map[string]func(*Config){"no-key": func(c *Config) { c.Keys = nil }, "short": func(c *Config) { c.Keys["v1"] = []byte("public") }, "unknown-active": func(c *Config) { c.ActiveKeyVersion = "v2" }, "no-validator": func(c *Config) { c.ValidateCreator = nil }, "long-ttl": func(c *Config) { c.TTL = 31 * 24 * time.Hour }, "no-ttl": func(c *Config) { c.TTL = 0 }, "capacity": func(c *Config) { c.Capacity = 0 }} {
		t.Run(name, func(t *testing.T) {
			c := testConfig()
			change(&c)
			_, err := New(db, c)
			assert.ErrorIs(t, err, ErrConfig)
		})
	}
}

func TestRegistrationCannotChangeCompletedItemSet(t *testing.T) {
	ctx := context.Background()
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	subset := c
	subset.OutputItemIDs = nil
	assert.ErrorIs(t, idx.Register(ctx, subset), ErrConflict)
	extension := c
	extension.OutputItemIDs = append(append([]string(nil), c.OutputItemIDs...), "extra-item")
	assert.ErrorIs(t, idx.Register(ctx, extension), ErrConflict)
	_, err := idx.Resolve(ctx, c.Owner, []Selector{{OutputItem, "extra-item"}})
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestMixedMemberAndOversizedLookupRemainUnavailable(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.ValidateCreator = func(_ context.Context, _ Owner, b Binding) (Target, error) {
		target := completion().Target
		if b.Provenance.CallID == "call-b" {
			target.MemberID = "member-b"
		}
		return target, nil
	}
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	other := c
	other.ResponseID = "resp-other"
	other.OutputItemIDs = nil
	other.Target.MemberID = "member-b"
	other.Provenance.CallID = "call-b"
	require.NoError(t, idx.Register(ctx, other))
	_, err := idx.Resolve(ctx, c.Owner, []Selector{{Response, c.ResponseID}, {Response, other.ResponseID}})
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestExactUnicodeAndCaseIDsCanCoexist(t *testing.T) {
	ctx := context.Background()
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	c := completion()
	c.OutputItemIDs = nil
	for n, id := range []string{"ABC", "abc", "é", "e\u0301"} {
		c.ResponseID = id
		c.Provenance.CallID = []string{"call-upper", "call-lower", "call-composed", "call-decomposed"}[n]
		require.NoError(t, idx.Register(ctx, c))
	}
	for n, id := range []string{"ABC", "abc", "é", "e\u0301"} {
		got, err := idx.Resolve(ctx, c.Owner, []Selector{{Response, id}})
		require.NoError(t, err)
		assert.Equal(t, []string{"call-upper", "call-lower", "call-composed", "call-decomposed"}[n], got.Bindings[0].Provenance.CallID)
	}
}
func TestDatabaseErrorsAreSanitized(t *testing.T) {
	ctx := context.Background()
	idx, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	assert.ErrorIs(t, idx.Register(ctx, completion()), ErrStorage)
	assert.ErrorIs(t, idx.Migrate(ctx), ErrStorage)
	_, err = idx.Cleanup(ctx, 1)
	assert.ErrorIs(t, err, ErrStorage)
}

func TestRetainedKeyVersionsResolveOneActualTarget(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	idx, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	first := completion()
	require.NoError(t, idx.Register(ctx, first))
	cfg.Keys["v2"] = bytes.Repeat([]byte{9}, 32)
	cfg.ActiveKeyVersion = "v2"
	next, err := New(db, cfg)
	require.NoError(t, err)
	require.NoError(t, next.Migrate(ctx))
	second := completion()
	second.ResponseID = "resp-v2"
	second.OutputItemIDs = nil
	second.Provenance.CallID = "call-v2"
	require.NoError(t, next.Register(ctx, second))
	got, err := next.Resolve(ctx, first.Owner, []Selector{{Response, first.ResponseID}, {Response, second.ResponseID}})
	require.NoError(t, err)
	assert.Equal(t, "root-a", got.RootTaskID)
	require.Len(t, got.Bindings, 2)
	assert.Equal(t, "v1", got.Bindings[0].KeyVersion)
	assert.Equal(t, "v2", got.Bindings[1].KeyVersion)
	assert.Equal(t, first.Target, got.Target)
}

func TestCreatingCallCannotPublishSecondCompletion(t *testing.T) {
	ctx := context.Background()
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), testConfig())
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	other := c
	other.ResponseID = "second-response"
	other.OutputItemIDs = []string{"second-item"}
	other.Provenance.ObservationDigest = strings.Repeat("b", 64)
	assert.ErrorIs(t, idx.Register(ctx, other), ErrConflict)
	_, err := idx.Resolve(ctx, c.Owner, selectors(other))
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestActualCreatorTargetMustMatchNamespaceCommitment(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	target := completion().Target
	cfg.ValidateCreator = func(context.Context, Owner, Binding) (Target, error) { return target, nil }
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	target.APIOrganization = "changed"
	assert.ErrorIs(t, idx.Register(ctx, c), ErrInvalid)
	target = c.Target
	require.NoError(t, idx.Register(ctx, c))
	target.Namespace = "https://changed.private/v1"
	_, err := idx.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
}
func TestAllowedPolicyBoundaryAndKeyringSnapshot(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.TTL = 30 * 24 * time.Hour
	idx, _ := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	cfg.Keys["v1"][0] = 1
	c := completion()
	c.Storage = CertifiedDefaultStored
	c.ResponseID = strings.Repeat("r", 255)
	c.ExpiresAt = testTime.Add(30 * 24 * time.Hour)
	c.OutputItemIDs = make([]string, 128)
	for n := range c.OutputItemIDs {
		c.OutputItemIDs[n] = fmt.Sprintf("item-%d", n)
	}
	require.NoError(t, idx.Register(ctx, c))
	got, err := idx.Resolve(ctx, c.Owner, []Selector{{Response, c.ResponseID}, {OutputItem, "item-127"}})
	require.NoError(t, err)
	require.Len(t, got.Bindings, 2)
	// Order has no semantic ownership effect; the exact immutable set is retained.
	for a, b := 0, len(c.OutputItemIDs)-1; a < b; a, b = a+1, b-1 {
		c.OutputItemIDs[a], c.OutputItemIDs[b] = c.OutputItemIDs[b], c.OutputItemIDs[a]
	}
	require.NoError(t, idx.Register(ctx, c))
}

func TestMissingRetainedKeyCannotTurnAmbiguityIntoGrant(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.ValidateCreator = func(_ context.Context, _ Owner, b Binding) (Target, error) {
		target := completion().Target
		if b.Provenance.CallID == "call-new-namespace" {
			target.Namespace = "https://new.private/v1"
		}
		return target, nil
	}
	idx, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	cfg.Keys["v2"] = bytes.Repeat([]byte{9}, 32)
	cfg.ActiveKeyVersion = "v2"
	next, err := New(db, cfg)
	require.NoError(t, err)
	require.NoError(t, next.Migrate(ctx))
	other := c
	other.Provenance.CallID = "call-new-namespace"
	other.Target.Namespace = "https://new.private/v1"
	require.NoError(t, next.Register(ctx, other))
	_, err = next.Resolve(ctx, c.Owner, selectors(c))
	require.ErrorIs(t, err, ErrUnavailable)
	delete(cfg.Keys, "v1")
	missing, err := New(db, cfg)
	require.NoError(t, err)
	_, err = missing.Resolve(ctx, c.Owner, selectors(c))
	assert.ErrorIs(t, err, ErrUnavailable)
}

// Two valid registrations are interleaved at the creator callback for A. Before
// those writes B is absent; afterwards A is ambiguous. A successful A+B result
// therefore cannot correspond to any coherent index snapshot.
func TestResolveCapturesCoherentSelectorAndKeyInventory(t *testing.T) {
	for _, newVersion := range []bool{false, true} {
		t.Run(fmt.Sprintf("new_key_version_%t", newVersion), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cfg := testConfig()
			base, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
			a := completion()
			a.OutputItemIDs = nil
			require.NoError(t, base.Register(ctx, a))
			writerConfig := testConfig()
			writerConfig.Keys["v2"] = bytes.Repeat([]byte{9}, 32)
			writerConfig.ValidateCreator = func(_ context.Context, _ Owner, b Binding) (Target, error) {
				target := completion().Target
				if b.Provenance.CallID == "call-ambiguous" {
					target.Namespace = "https://other.private/v1"
				}
				return target, nil
			}
			if newVersion {
				writerConfig.ActiveKeyVersion = "v2"
			}
			writer, err := New(db, writerConfig)
			require.NoError(t, err)
			reached, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			cfg.ValidateCreator = func(ctx context.Context, o Owner, b Binding) (Target, error) {
				if b.Provenance.CallID == a.Provenance.CallID {
					once.Do(func() { close(reached) })
					select {
					case <-resume:
					case <-ctx.Done():
						return Target{}, ctx.Err()
					}
				}
				return completion().Target, nil
			}
			reader, err := New(db, cfg)
			require.NoError(t, err)
			result := make(chan error, 1)
			go func() {
				_, err := reader.Resolve(ctx, a.Owner, []Selector{{Response, a.ResponseID}, {Response, "response-later"}})
				result <- err
			}()
			select {
			case err := <-result:
				// A coherent pre-write snapshot observes B as missing and never needs to
				// call the external creator validator for an incomplete candidate set.
				assert.ErrorIs(t, err, ErrUnavailable)
			case <-reached:
				ambiguous := a
				ambiguous.Provenance.CallID = "call-ambiguous"
				ambiguous.Target.Namespace = "https://other.private/v1"
				require.NoError(t, writer.Register(ctx, ambiguous))
				writerConfig.ActiveKeyVersion = "v1"
				writer, err = New(db, writerConfig)
				require.NoError(t, err)
				b := a
				b.ResponseID = "response-later"
				b.Provenance.CallID = "call-later"
				require.NoError(t, writer.Register(ctx, b))
				close(resume)
				assert.ErrorIs(t, <-result, ErrUnavailable)
			case <-ctx.Done():
				t.Fatal("resolution did not complete or reach creator validation")
			}
		})
	}
}

// Capturing rows may hold the index gate; validation of immutable parent records
// must run after it releases the sole SQLite connection. No elapsed-time claim
// is made: the context deadline is only a deadlock watchdog.
func TestResolveCreatorCanReadSingleConnectionParentDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := testConfig()
	idx, db := openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	c := completion()
	require.NoError(t, idx.Register(ctx, c))
	cfg.ValidateCreator = func(ctx context.Context, _ Owner, _ Binding) (Target, error) {
		var one int
		err := db.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error
		if err != nil {
			return Target{}, err
		}
		if one != 1 {
			return Target{}, errors.New("parent read failed")
		}
		return c.Target, nil
	}
	reader, err := New(db, cfg)
	require.NoError(t, err)
	got, err := reader.Resolve(ctx, c.Owner, selectors(c))
	require.NoError(t, err)
	assert.Equal(t, c.Target, got.Target)
}
