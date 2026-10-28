package continuityindex

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// All indexed strings are fixed-length lowercase ASCII commitments. The two
// column owner/selector index occupies at most 512 bytes under utf8mb4, below
// MySQL 5.7's legacy 767-byte key limit. Time is persisted as Unix milliseconds
// so all three database engines preserve exactly the same immutable record.
type referenceRow struct {
	CompletionDigest  string `gorm:"size:64;not null"`
	ReferenceKey      string `gorm:"primaryKey;size:64"`
	SelectorDigest    string `gorm:"size:64;not null;index:idx_native_owner_selector,priority:2"`
	OwnerDigest       string `gorm:"size:64;not null;index:idx_native_owner_selector,priority:1"`
	RootDigest        string `gorm:"size:64;not null"`
	KeyVersion        string `gorm:"size:32;not null;index"`
	KeyFingerprint    string `gorm:"size:64;not null"`
	RootTaskID        string `gorm:"size:128;not null"`
	Kind              string `gorm:"size:16;not null"`
	ResponseKey       string `gorm:"size:64;not null"`
	ChannelID         int64  `gorm:"not null"`
	MemberID          string `gorm:"size:128;not null"`
	PoolID            string `gorm:"size:128;not null"`
	NamespaceDigest   string `gorm:"size:64;not null"`
	CallID            string `gorm:"size:128;not null;index"`
	AttemptID         int64  `gorm:"not null"`
	PublicModel       string `gorm:"size:255;not null"`
	UpstreamModel     string `gorm:"size:255;not null"`
	Wire              string `gorm:"size:32;not null"`
	ObservationDigest string `gorm:"size:64;not null"`
	PolicyVersion     string `gorm:"size:128;not null"`
	Storage           string `gorm:"size:32;not null"`
	CreatedMS         int64  `gorm:"not null"`
	ExpiresMS         int64  `gorm:"not null;index"`
}

func (referenceRow) TableName() string { return "platform_native_history_references" }

// A write to the singleton gate serializes count-and-insert and cleanup across
// processes. It is an ordinary UPDATE, portable to SQLite/MySQL/PostgreSQL;
// no SELECT FOR UPDATE or process-local financial/capacity counter is used.
type capacityGate struct {
	ID       int64 `gorm:"primaryKey;autoIncrement:false"`
	Capacity int64 `gorm:"not null"`
	Tick     int64 `gorm:"not null"`
}

func (capacityGate) TableName() string { return "platform_native_history_capacity" }

func New(db *gorm.DB, c Config) (*Index, error) {
	if db == nil || c.ValidateCreator == nil || c.TTL < time.Millisecond || c.TTL > 30*24*time.Hour || c.Capacity < 1 || c.Capacity > 10_000_000 || len(c.Keys) < 1 || len(c.Keys) > 16 {
		return nil, ErrConfig
	}
	keys := make(map[string][]byte, len(c.Keys))
	for v, key := range c.Keys {
		if len(v) < 1 || len(v) > 32 || len(key) < 32 || len(key) > 4096 {
			return nil, ErrConfig
		}
		for _, r := range v {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return nil, ErrConfig
			}
		}
		keys[v] = append([]byte(nil), key...)
	}
	if _, ok := keys[c.ActiveKeyVersion]; !ok {
		return nil, ErrConfig
	}
	c.Keys = keys
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Index{db: db, config: c}, nil
}

func (i *Index) Migrate(ctx context.Context) error {
	db := i.db.WithContext(ctx)
	if err := db.AutoMigrate(&referenceRow{}, &capacityGate{}); err != nil {
		return ErrStorage
	}
	return storageResult(db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&capacityGate{ID: 1, Capacity: i.config.Capacity}).Error; err != nil {
			return ErrStorage
		}
		if err := i.acquireGate(tx); err != nil {
			return err
		}
		return i.validateRetainedKeys(tx, false)
	}))
}

func (i *Index) acquireGate(tx *gorm.DB) error {
	// Toggle rather than increment: no arithmetic overflow after long uptime,
	// and MySQL reports one changed row even without CLIENT_FOUND_ROWS.
	r := tx.Model(&capacityGate{}).Where("id = ? AND capacity = ?", 1, i.config.Capacity).UpdateColumn("tick", gorm.Expr("1 - tick"))
	if r.Error != nil {
		return ErrStorage
	}
	if r.RowsAffected != 1 {
		return ErrConfig
	}
	return nil
}

// Retained records bind a key version to a private-key commitment. Registration
// and lookup must know every retained key to check collisions and ambiguous
// selectors across rotations. Migration may tolerate a missing retained key so
// a private cleanup can recover capacity; lookup/registration still fail closed.
func (i *Index) validateRetainedKeys(tx *gorm.DB, requireAll bool) error {
	var versions []referenceRow
	if err := tx.Model(&referenceRow{}).Distinct("key_version", "key_fingerprint").Limit(17).Find(&versions).Error; err != nil {
		return ErrStorage
	}
	if len(versions) > 16 {
		return ErrConfig
	}
	for _, v := range versions {
		if _, ok := i.config.Keys[v.KeyVersion]; !ok {
			if requireAll {
				return ErrStorage
			}
			continue
		}
		if !hmac.Equal([]byte(v.KeyFingerprint), []byte(i.digest(v.KeyVersion, "key-fingerprint"))) {
			return ErrConfig
		}
	}
	return nil
}

// digest frames every field with its byte length; ambiguous concatenations,
// Unicode normalization and case-folding must never identify the same resource.
func (i *Index) digest(version, domain string, parts ...string) string {
	h := hmac.New(sha256.New, i.config.Keys[version])
	all := append([]string{"native-history-index/v1", version, domain}, parts...)
	var size [8]byte
	for _, s := range all {
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (i *Index) ownerDigest(v string, o Owner) string {
	credential := ""
	if o.Mode == DurableKey {
		credential = o.CredentialID
	}
	return i.digest(v, "owner", o.ActorID, o.AccountID, o.ProductID, o.InstanceID, string(o.Mode), credential)
}
func bounded(s string, max int, optional bool) bool {
	if (!optional && s == "") || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validOwner(o Owner, registration bool) bool {
	for _, s := range []string{o.ActorID, o.AccountID, o.ProductID, o.InstanceID, o.CredentialID} {
		if !bounded(s, 128, false) {
			return false
		}
	}
	if o.Mode != TaskCredential && o.Mode != DurableKey {
		return false
	}
	// Key roots are ignored at lookup, never validated or used as authority.
	return !registration && o.Mode == DurableKey || bounded(o.RootTaskID, 128, false)
}
func validTarget(t Target) bool {
	return t.ChannelID > 0 && bounded(t.MemberID, 128, false) && bounded(t.PoolID, 128, true) && bounded(t.Namespace, 2048, false) && bounded(t.APIOrganization, 255, true)
}
func (i *Index) completionRows(c TrustedCompletion, version string) ([]referenceRow, error) {
	now := i.config.Now().UTC()
	if !validOwner(c.Owner, true) || !validTarget(c.Target) || !c.Completed || c.Provenance.Wire != "responses.create" || (c.Storage != ExplicitStored && c.Storage != CertifiedDefaultStored) || !bounded(c.ResponseID, 255, false) || len(c.OutputItemIDs) > 128 {
		return nil, ErrInvalid
	}
	p := c.Provenance
	for _, s := range []string{p.CallID, p.PolicyVersion} {
		if !bounded(s, 128, false) {
			return nil, ErrInvalid
		}
	}
	if !bounded(p.PublicModel, 255, false) || !bounded(p.UpstreamModel, 255, false) || p.AttemptID == 0 || p.AttemptID > uint64(1<<63-1) || len(p.ObservationDigest) != 64 {
		return nil, ErrInvalid
	}
	decoded, err := hex.DecodeString(p.ObservationDigest)
	if err != nil || len(decoded) != 32 || strings.ToLower(p.ObservationDigest) != p.ObservationDigest {
		return nil, ErrInvalid
	}
	if c.CreatedAt.Before(time.Unix(0, 0)) || c.CreatedAt.After(now) || !c.ExpiresAt.After(now) || !c.ExpiresAt.After(c.CreatedAt) || c.ExpiresAt.Sub(c.CreatedAt) > i.config.TTL || !c.CreatedAt.Equal(c.CreatedAt.Truncate(time.Millisecond)) || !c.ExpiresAt.Equal(c.ExpiresAt.Truncate(time.Millisecond)) {
		return nil, ErrInvalid
	}
	owner := i.ownerDigest(version, c.Owner)
	namespace := i.digest(version, "namespace", c.Target.Namespace, c.Target.APIOrganization)
	responseKey := i.digest(version, "resource", namespace, c.Target.MemberID, string(Response), c.ResponseID)
	template := referenceRow{OwnerDigest: owner, RootDigest: i.digest(version, "root", owner, c.Owner.RootTaskID), RootTaskID: c.Owner.RootTaskID, KeyVersion: version, KeyFingerprint: i.digest(version, "key-fingerprint"), ResponseKey: responseKey, ChannelID: c.Target.ChannelID, MemberID: c.Target.MemberID, PoolID: c.Target.PoolID, NamespaceDigest: namespace, CallID: p.CallID, AttemptID: int64(p.AttemptID), PublicModel: p.PublicModel, UpstreamModel: p.UpstreamModel, Wire: p.Wire, ObservationDigest: p.ObservationDigest, PolicyVersion: p.PolicyVersion, Storage: string(c.Storage), CreatedMS: c.CreatedAt.UnixMilli(), ExpiresMS: c.ExpiresAt.UnixMilli()}
	items := []Selector{{Response, c.ResponseID}}
	seen := make(map[string]bool, len(c.OutputItemIDs))
	for _, id := range c.OutputItemIDs {
		if !bounded(id, 255, false) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		items = append(items, Selector{OutputItem, id})
	}
	sortedIDs := append([]string(nil), c.OutputItemIDs...)
	sort.Strings(sortedIDs)
	template.CompletionDigest = i.digest(version, "completion-set", append([]string{c.ResponseID}, sortedIDs...)...)
	rows := make([]referenceRow, 0, len(items))
	for _, s := range items {
		row := template
		row.Kind = string(s.Kind)
		row.ReferenceKey = i.digest(version, "resource", namespace, c.Target.MemberID, string(s.Kind), s.ID)
		row.SelectorDigest = i.digest(version, "selector", string(s.Kind), s.ID)
		rows = append(rows, row)
	}
	return rows, nil
}
func (r referenceRow) binding() Binding {
	return Binding{ReferenceKey: r.ReferenceKey, SelectorDigest: r.SelectorDigest, OwnerDigest: r.OwnerDigest, RootDigest: r.RootDigest, KeyVersion: r.KeyVersion, RootTaskID: r.RootTaskID, Kind: ResourceKind(r.Kind), ResponseKey: r.ResponseKey, ChannelID: r.ChannelID, MemberID: r.MemberID, PoolID: r.PoolID, NamespaceDigest: r.NamespaceDigest, Provenance: Provenance{CallID: r.CallID, AttemptID: uint64(r.AttemptID), PublicModel: r.PublicModel, UpstreamModel: r.UpstreamModel, Wire: r.Wire, ObservationDigest: r.ObservationDigest, PolicyVersion: r.PolicyVersion}, Storage: StoragePolicy(r.Storage), CreatedAt: time.UnixMilli(r.CreatedMS).UTC(), ExpiresAt: time.UnixMilli(r.ExpiresMS).UTC()}
}
func (i *Index) Register(ctx context.Context, c TrustedCompletion) error {
	rows, err := i.completionRows(c, i.config.ActiveKeyVersion)
	if err != nil {
		return err
	}
	for _, r := range rows {
		target, err := i.config.ValidateCreator(ctx, c.Owner, r.binding())
		if err != nil || !i.matchesTarget(r, target) {
			return ErrInvalid
		}
	}
	return storageResult(i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := i.acquireGate(tx); err != nil {
			return err
		}
		if err := i.validateRetainedKeys(tx, true); err != nil {
			return err
		}
		// Compute every retained key version before any insert. The global resource
		// key excludes owner: another owner cannot claim an already observed ID.
		versions := make([]string, 0, len(i.config.Keys))
		for v := range i.config.Keys {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		matched := make([]bool, len(rows))
		for _, v := range versions {
			expected, err := i.completionRows(c, v)
			if err != nil {
				return err
			}
			keys := make([]string, len(expected))
			for n, r := range expected {
				keys[n] = r.ReferenceKey
			}
			var existing []referenceRow
			if err := tx.Where("reference_key IN ?", keys).Find(&existing).Error; err != nil {
				return ErrStorage
			}
			byKey := make(map[string]referenceRow, len(existing))
			for _, r := range existing {
				byKey[r.ReferenceKey] = r
			}
			for n, r := range expected {
				if old, ok := byKey[r.ReferenceKey]; ok {
					if old != r || matched[n] {
						return ErrConflict
					}
					matched[n] = true
				}
			}
		}
		pending := make([]referenceRow, 0, len(rows))
		for n, r := range rows {
			if !matched[n] {
				pending = append(pending, r)
			}
		}

		// One creating Call has one immutable completed resource set. This also
		// rejects a second all-new response ID under the same completion proof.
		var priorCount int64
		if err := tx.Model(&referenceRow{}).Where("call_id = ?", c.Provenance.CallID).Count(&priorCount).Error; err != nil {
			return ErrStorage
		}
		if priorCount > 0 && (priorCount != int64(len(rows)) || len(pending) > 0) {
			return ErrConflict
		}
		if len(pending) == 0 {
			return nil
		}
		// A different item list must not expand a previously registered immutable
		// completion, even when all old rows would themselves compare equal.
		if len(pending) != len(rows) {
			return ErrConflict
		}
		var count int64
		if err := tx.Model(&referenceRow{}).Count(&count).Error; err != nil {
			return ErrStorage
		}
		if count > i.config.Capacity-int64(len(pending)) {
			return ErrCapacity
		}
		if err := tx.Create(&pending).Error; err != nil {
			return ErrStorage
		}
		return nil
	}))
}
func (i *Index) Resolve(ctx context.Context, o Owner, selectors []Selector) (Resolution, error) {
	if !validOwner(o, false) || len(selectors) < 1 || len(selectors) > 64 {
		return Resolution{}, ErrUnavailable
	}

	for _, s := range selectors {
		if (s.Kind != Response && s.Kind != OutputItem) || !bounded(s.ID, 255, false) {
			return Resolution{}, ErrUnavailable
		}
	}
	// Capture the key inventory and all selectors under the same gate as writes.
	// A plain transaction would still permit a fractured read on PostgreSQL's
	// READ COMMITTED default. The portable gate excludes Register and Cleanup
	// while this bounded set is captured, including across key versions.
	captured := make([]referenceRow, 0, len(selectors))
	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := i.acquireGate(tx); err != nil {
			return err
		}
		// Ignoring an unavailable key can hide another namespace match and turn an
		// ambiguous selector into a grant. Inventory belongs to this same snapshot.
		if err := i.validateRetainedKeys(tx, true); err != nil {
			return err
		}
		now := i.config.Now().UnixMilli()
		for _, s := range selectors {
			var matches []referenceRow
			for v := range i.config.Keys {
				var rows []referenceRow
				if err := tx.Where("owner_digest = ? AND selector_digest = ? AND key_version = ? AND expires_ms > ?", i.ownerDigest(v, o), i.digest(v, "selector", string(s.Kind), s.ID), v, now).Limit(2).Find(&rows).Error; err != nil {
					return ErrUnavailable
				}
				matches = append(matches, rows...)
				if len(matches) > 1 {
					return ErrUnavailable
				}
			}
			if len(matches) != 1 {
				return ErrUnavailable
			}
			captured = append(captured, matches[0])
		}
		return nil
	})
	if err != nil {
		return Resolution{}, ErrUnavailable
	}
	// Creator validation reads immutable parent records through a separate API.
	// Release the gate and DB connection first; callbacks may use a one-connection
	// SQLite parent DB. Current grants/final send eligibility remain 3D3B checks.
	result := Resolution{Bindings: make([]Binding, 0, len(captured))}
	var selectedTarget Target
	haveTarget := false
	for n, r := range captured {
		s := selectors[n]
		if !hmac.Equal([]byte(r.KeyFingerprint), []byte(i.digest(r.KeyVersion, "key-fingerprint"))) || r.Kind != string(s.Kind) || r.Wire != "responses.create" || (r.Storage != string(ExplicitStored) && r.Storage != string(CertifiedDefaultStored)) || r.RootDigest != i.digest(r.KeyVersion, "root", r.OwnerDigest, r.RootTaskID) || !bounded(r.RootTaskID, 128, false) {
			return Resolution{}, ErrUnavailable
		}
		if o.Mode == TaskCredential && r.RootTaskID != o.RootTaskID {
			return Resolution{}, ErrUnavailable
		}
		if result.RootTaskID != "" && result.RootTaskID != r.RootTaskID {
			return Resolution{}, ErrUnavailable
		}

		owner := o
		owner.RootTaskID = r.RootTaskID
		b := r.binding()
		target, err := i.config.ValidateCreator(ctx, owner, b)
		if err != nil || !i.matchesTarget(r, target) {
			return Resolution{}, ErrUnavailable
		}
		if haveTarget && target != selectedTarget {
			return Resolution{}, ErrUnavailable
		}
		selectedTarget = target
		haveTarget = true
		result.RootTaskID = r.RootTaskID
		result.Bindings = append(result.Bindings, b)
	}
	result.Target = selectedTarget
	return result, nil
}
func (i *Index) Cleanup(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 500 {
		return 0, ErrInvalid
	}
	var removed int64
	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := i.acquireGate(tx); err != nil {
			return err
		}
		var expired []referenceRow
		if err := tx.Select("reference_key").Where("expires_ms <= ?", i.config.Now().UnixMilli()).Order("expires_ms, reference_key").Limit(limit).Find(&expired).Error; err != nil {
			return ErrStorage
		}
		if len(expired) == 0 {
			return nil
		}
		keys := make([]string, len(expired))
		for n, r := range expired {
			keys[n] = r.ReferenceKey
		}
		r := tx.Where("reference_key IN ?", keys).Delete(&referenceRow{})
		if r.Error != nil {
			return ErrStorage
		}
		removed = r.RowsAffected
		return nil
	})
	if err != nil {
		return 0, storageResult(err)
	}
	return removed, nil
}

// Database errors may contain SQL or driver details; expose only stable private
// classes, never interpolate provider IDs, credential material or driver text.
func storageResult(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrConfig, ErrConflict, ErrCapacity, ErrInvalid, ErrStorage} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrStorage
}

// Namespace is loaded from the verified original send receipt, not from caller
// metadata. Recompute with each record's key so retained rotations remain usable
// without persisting a recoverable namespace or weakening target equality.
func (i *Index) matchesTarget(r referenceRow, t Target) bool {
	return validTarget(t) && r.ChannelID == t.ChannelID && r.MemberID == t.MemberID && r.PoolID == t.PoolID && hmac.Equal([]byte(r.NamespaceDigest), []byte(i.digest(r.KeyVersion, "namespace", t.Namespace, t.APIOrganization)))
}
