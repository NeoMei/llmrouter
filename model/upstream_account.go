package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpstreamAccountRef is a safe management handle. Credentials remain in their existing stores.
type UpstreamAccountRef struct {
	PoolID     string `json:"pool_id"`
	MemberID   string `json:"member_id"`
	ChannelID  int    `json:"channel_id"`
	KeyIndex   int    `json:"key_index"`
	QuotaGroup string `json:"quota_group,omitempty"`
}

type UpstreamAccountScope struct {
	Operation  string
	Model      string
	QuotaGroup string
}
type UpstreamAccountSelection struct {
	Ref        UpstreamAccountRef `json:"ref"`
	KeyIndex   int                `json:"key_index"`
	SelectedAt time.Time          `json:"selected_at"`
	Sequence   int64              `json:"-"`
}
type UpstreamAccountUnavailable struct {
	RetryAt time.Time `json:"retry_at,omitempty"`
}

func (*UpstreamAccountUnavailable) Error() string { return "upstream account pool unavailable" }

type UpstreamAccountFailureKind string

const (
	UpstreamAccountCooldown UpstreamAccountFailureKind = "cooldown"
	UpstreamAccountInvalid  UpstreamAccountFailureKind = "invalid"
)

type UpstreamAccountFailure struct {
	Kind    UpstreamAccountFailureKind
	Reason  string
	RetryAt time.Time
}

type UpstreamAccountPool struct {
	PoolID   string `gorm:"primaryKey;size:112" json:"pool_id"`
	Sequence int64  `json:"-"`
}
type UpstreamAccountMember struct {
	MemberID            string `gorm:"primaryKey;size:36" json:"member_id"`
	PoolID              string `gorm:"size:96;uniqueIndex:idx_upstream_member_identity,priority:1" json:"pool_id"`
	IdentityDigest      string `gorm:"size:64;uniqueIndex:idx_upstream_member_identity,priority:2" json:"-"`
	LastSelected        int64  `json:"-"`
	QuotaGroup          string `gorm:"size:96" json:"quota_group,omitempty"`
	MembershipUpdatedAt int64  `json:"membership_updated_at,omitempty"`
}
type UpstreamAccountState struct {
	ID        string                     `gorm:"primaryKey;size:64" json:"-"`
	PoolID    string                     `gorm:"size:112;index" json:"pool_id"`
	Subject   string                     `gorm:"size:112" json:"subject"`
	Operation string                     `gorm:"type:text" json:"operation,omitempty"`
	Model     string                     `gorm:"type:text" json:"model,omitempty"`
	Kind      UpstreamAccountFailureKind `gorm:"size:16" json:"kind,omitempty"`
	Reason    string                     `gorm:"size:32" json:"reason,omitempty"`
	RetryAt   int64                      `json:"retry_at,omitempty"`
	Paused    bool                       `json:"paused"`
	Revision  int64                      `json:"-"`
	UpdatedAt int64                      `gorm:"autoUpdateTime:false" json:"updated_at"`
}

// ResolveUpstreamAccountRef maps a private identity to an opaque, persistent ID.
// Native keys use the credential as identity. Gateway uses a row ID plus credential
// generation. Neither the identity nor its private digest may be exposed to clients.
// Pool and quota-group names must contain safe management identifiers only.
func ResolveUpstreamAccountRef(ctx context.Context, db *gorm.DB, poolID string, channelID, keyIndex int, identity, quotaGroup string) (UpstreamAccountRef, error) {
	ref := UpstreamAccountRef{PoolID: poolID, ChannelID: channelID, KeyIndex: keyIndex, QuotaGroup: quotaGroup}
	if db == nil || !safeAccountID(poolID) || (quotaGroup != "" && !safeAccountID(quotaGroup)) || identity == "" || channelID <= 0 || keyIndex < 0 {
		return UpstreamAccountRef{}, errors.New("invalid upstream account identity")
	}
	digest := accountStateID(poolID, identity)
	member := UpstreamAccountMember{MemberID: uuid.NewString(), PoolID: poolID, IdentityDigest: digest, QuotaGroup: quotaGroup}
	err := db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "pool_id"}, {Name: "identity_digest"}}, DoNothing: true}).Create(&member).Error
	if err != nil {
		return UpstreamAccountRef{}, fmt.Errorf("resolve upstream account: %w", err)
	}
	member = UpstreamAccountMember{}
	if err = db.WithContext(ctx).Where("pool_id = ? AND identity_digest = ?", poolID, digest).First(&member).Error; err != nil {
		return UpstreamAccountRef{}, fmt.Errorf("resolve upstream account: %w", err)
	}
	ref.MemberID = member.MemberID
	ref.QuotaGroup = member.QuotaGroup
	return ref, nil
}

// SelectUpstreamAccount only considers the authorized candidates supplied by its
// caller. Exclusion and credential/authorization filtering are request-owned.
// The transaction ends before the caller performs any network I/O.
func SelectUpstreamAccount(ctx context.Context, db *gorm.DB, refs []UpstreamAccountRef, scope UpstreamAccountScope, excluded map[string]bool, now time.Time) (UpstreamAccountSelection, error) {
	if len(refs) == 0 {
		return UpstreamAccountSelection{}, &UpstreamAccountUnavailable{}
	}
	if err := validateAccountScope(scope); err != nil {
		return UpstreamAccountSelection{}, err
	}
	poolID := refs[0].PoolID
	var selected UpstreamAccountSelection
	var unavailable *UpstreamAccountUnavailable
	err := accountPoolTransaction(ctx, db, poolID, nil, func(tx *gorm.DB, revision int64) error {
		var members []UpstreamAccountMember
		ids := make([]string, 0, len(refs))
		for _, ref := range refs {
			if ref.PoolID != poolID {
				return errors.New("mixed upstream account pools")
			}
			if err := validateAccountRef(ref); err != nil {
				return err
			}
			ids = append(ids, ref.MemberID)
		}
		if err := tx.Where("pool_id = ? AND member_id IN ?", poolID, ids).Find(&members).Error; err != nil {
			return err
		}
		lastSelected := make(map[string]int64, len(members))
		memberGroups := make(map[string]string, len(members))
		groups := make([]string, 0, len(members))
		for _, member := range members {
			lastSelected[member.MemberID] = member.LastSelected
			memberGroups[member.MemberID] = member.QuotaGroup
			groups = append(groups, member.QuotaGroup)
		}
		if err := lockAccountQuotaGroups(tx, groups); err != nil {
			return err
		}
		namespaces := []string{"pool:" + poolID}
		for _, group := range groups {
			if group != "" {
				namespaces = append(namespaces, "quota:"+group)
			}
		}
		var states []UpstreamAccountState
		stateQuery := tx
		// MySQL REPEATABLE READ may already have a membership-read snapshot from
		// before waiting on a shared group. A locking read sees the committed latest
		// failure after the coordinator is acquired, rather than that older snapshot.
		if tx.Dialector.Name() != "sqlite" {
			stateQuery = lockForUpdate(tx)
		}
		if err := stateQuery.Where("pool_id IN ?", namespaces).Find(&states).Error; err != nil {
			return err
		}
		found := false
		earliest := int64(0)
		for _, ref := range refs {
			ref.QuotaGroup = memberGroups[ref.MemberID]
			if excluded[ref.MemberID] {
				continue
			}
			sequence, exists := lastSelected[ref.MemberID]
			if !exists {
				return errors.New("unknown upstream account member")
			}
			blocked, permanent, retryAt := false, false, int64(0)
			for _, state := range states {
				matches := state.Subject == "member:"+ref.MemberID || (ref.QuotaGroup != "" && state.Subject == "quota:"+ref.QuotaGroup)
				if !matches || (state.Operation != "" && state.Operation != scope.Operation) || (state.Model != "" && state.Model != scope.Model) {
					continue
				}
				if state.Paused || state.Kind == UpstreamAccountInvalid {
					blocked = true
					permanent = true
				}
				if state.Kind == UpstreamAccountCooldown && state.RetryAt > now.UnixNano() {
					blocked = true
					if state.RetryAt > retryAt {
						retryAt = state.RetryAt
					}
				}
			}
			if blocked {
				if !permanent && retryAt > 0 && (earliest == 0 || retryAt < earliest) {
					earliest = retryAt
				}
				continue
			}
			if !found || sequence < lastSelected[selected.Ref.MemberID] {
				found = true
				selected = UpstreamAccountSelection{Ref: ref, KeyIndex: ref.KeyIndex, SelectedAt: now, Sequence: revision}
			}
		}
		if !found {
			unavailable = &UpstreamAccountUnavailable{}
			if earliest > 0 {
				unavailable.RetryAt = time.Unix(0, earliest).UTC()
			}
			return nil
		}
		return tx.Model(&UpstreamAccountMember{}).Where("pool_id = ? AND member_id = ?", poolID, selected.Ref.MemberID).Update("last_selected", revision).Error
	})
	if err != nil {
		return UpstreamAccountSelection{}, fmt.Errorf("select upstream account: %w", err)
	}
	if unavailable != nil {
		return UpstreamAccountSelection{}, unavailable
	}
	return selected, nil
}

// RecordUpstreamAccountFailure uses the immutable sent-member receipt, never a
// channel's current key index. QuotaGroup must match that receipt's configured group.
func RecordUpstreamAccountFailure(ctx context.Context, db *gorm.DB, sent UpstreamAccountSelection, scope UpstreamAccountScope, failure UpstreamAccountFailure, now time.Time) error {
	if err := validateAccountRef(sent.Ref); err != nil {
		return err
	}
	if err := validateAccountScope(scope); err != nil {
		return err
	}
	if failure.Kind != UpstreamAccountCooldown && failure.Kind != UpstreamAccountInvalid {
		return errors.New("invalid upstream account failure kind")
	}
	if (failure.Kind == UpstreamAccountInvalid && failure.Reason != "invalid_credential") || (failure.Kind == UpstreamAccountCooldown && failure.Reason != "rate_limit" && failure.Reason != "quota_exhausted") {
		return errors.New("invalid upstream account failure reason")
	}
	if failure.Kind == UpstreamAccountInvalid {
		scope = UpstreamAccountScope{}
	}
	if failure.Kind == UpstreamAccountCooldown && (!failure.RetryAt.After(now) || failure.RetryAt.Sub(now) > 32*24*time.Hour) {
		return errors.New("invalid upstream account cooldown")
	}
	subject, err := accountScopeSubject(sent.Ref, scope)
	if err != nil {
		return err
	}
	return accountPoolTransaction(ctx, db, sent.Ref.PoolID, []string{scope.QuotaGroup}, func(tx *gorm.DB, revision int64) error {
		namespace := "pool:" + sent.Ref.PoolID
		if scope.QuotaGroup != "" {
			namespace = "quota:" + scope.QuotaGroup
		}
		state, err := loadAccountState(tx, namespace, subject, scope)
		if err != nil {
			return err
		}
		if state.Kind != UpstreamAccountInvalid {
			state.Kind = failure.Kind
			state.Reason = failure.Reason
			if failure.RetryAt.UnixNano() > state.RetryAt {
				state.RetryAt = failure.RetryAt.UnixNano()
			}
		}
		state.Revision = revision
		state.UpdatedAt = now.UnixNano()
		return tx.Save(&state).Error
	})
}

// A success only clears older temporary member failures applicable to its scope.
// It cannot clear a pause, invalid credentials, a shared-quota refusal, or a failure
// recorded after this attempt was selected (even with identical timestamps).
func RecordUpstreamAccountSuccess(ctx context.Context, db *gorm.DB, sent UpstreamAccountSelection, scope UpstreamAccountScope, now time.Time) error {
	if err := validateAccountRef(sent.Ref); err != nil {
		return err
	}
	if err := validateAccountScope(scope); err != nil {
		return err
	}
	if sent.Sequence <= 0 {
		return errors.New("invalid upstream account receipt")
	}
	return accountPoolTransaction(ctx, db, sent.Ref.PoolID, []string{scope.QuotaGroup}, func(tx *gorm.DB, revision int64) error {
		return tx.Model(&UpstreamAccountState{}).Where("pool_id = ? AND subject = ? AND kind = ? AND revision <= ?", "pool:"+sent.Ref.PoolID, "member:"+sent.Ref.MemberID, UpstreamAccountCooldown, sent.Sequence).
			Where("(operation = ? OR operation = '') AND (model = ? OR model = '')", scope.Operation, scope.Model).
			Updates(map[string]interface{}{"kind": "", "reason": "", "retry_at": 0, "revision": revision, "updated_at": now.UnixNano()}).Error
	})
}

func SetUpstreamAccountPaused(ctx context.Context, db *gorm.DB, ref UpstreamAccountRef, paused bool, now time.Time) error {
	if err := validateAccountRef(ref); err != nil {
		return err
	}
	return accountPoolTransaction(ctx, db, ref.PoolID, nil, func(tx *gorm.DB, revision int64) error {
		state, err := loadAccountState(tx, "pool:"+ref.PoolID, "member:"+ref.MemberID, UpstreamAccountScope{})
		if err != nil {
			return err
		}
		state.Paused = paused
		state.Revision = revision
		state.UpdatedAt = now.UnixNano()
		return tx.Save(&state).Error
	})
}

// RecoverUpstreamAccount explicitly clears automatic state for the exact scope,
// including an invalid-credential isolation at the account-global scope. Pauses
// require a separate explicit resume. A quota-group scope recovers that group.
func RecoverUpstreamAccount(ctx context.Context, db *gorm.DB, ref UpstreamAccountRef, scope UpstreamAccountScope, now time.Time) error {
	if err := validateAccountRef(ref); err != nil {
		return err
	}
	if err := validateAccountScope(scope); err != nil {
		return err
	}
	subject, err := accountScopeSubject(ref, scope)
	if err != nil {
		return err
	}
	return accountPoolTransaction(ctx, db, ref.PoolID, []string{scope.QuotaGroup}, func(tx *gorm.DB, revision int64) error {
		namespace := "pool:" + ref.PoolID
		if scope.QuotaGroup != "" {
			namespace = "quota:" + scope.QuotaGroup
		}
		return tx.Model(&UpstreamAccountState{}).Where("id = ?", accountStateID(namespace, subject, scope.Operation, scope.Model)).
			Updates(map[string]interface{}{"kind": "", "reason": "", "retry_at": 0, "revision": revision, "updated_at": now.UnixNano()}).Error
	})
}

// accountPoolTransaction acquires the shared coordinator with a real write BEFORE
// any health reads. This serializes SQLite writers as well as MySQL/PostgreSQL
// selectors and health mutations; no process-local mutex or read-then-write race.
func accountPoolTransaction(ctx context.Context, db *gorm.DB, poolID string, groups []string, apply func(*gorm.DB, int64) error) error {
	if db == nil || !safeAccountID(poolID) {
		return errors.New("invalid upstream account database or pool")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pool, err := lockAccountCoordinator(tx, "pool:"+poolID)
		if err != nil {
			return err
		}
		if err = lockAccountQuotaGroups(tx, groups); err != nil {
			return err
		}
		return apply(tx, pool.Sequence)
	})
}

// Lock ordering is always the one owning pool, then sorted unique quota groups.
// No transaction locks another pool after a group, including membership changes.
func lockAccountQuotaGroups(tx *gorm.DB, groups []string) error {
	unique := make(map[string]bool, len(groups))
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		if group == "" || unique[group] {
			continue
		}
		if !safeAccountID(group) {
			return errors.New("invalid upstream quota group")
		}
		unique[group] = true
		ids = append(ids, "quota:"+group)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := lockAccountCoordinator(tx, id); err != nil {
			return err
		}
	}
	return nil
}

func lockAccountCoordinator(tx *gorm.DB, id string) (UpstreamAccountPool, error) {
	pool := UpstreamAccountPool{PoolID: id}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&pool).Error; err != nil {
		return pool, err
	}
	if err := tx.Model(&pool).Where("pool_id = ?", id).Update("sequence", gorm.Expr("sequence + ?", 1)).Error; err != nil {
		return pool, err
	}
	query := tx
	if tx.Dialector.Name() != "sqlite" {
		query = lockForUpdate(tx)
	}
	err := query.First(&pool, "pool_id = ?", id).Error
	return pool, err
}

func loadAccountState(tx *gorm.DB, poolID, subject string, scope UpstreamAccountScope) (UpstreamAccountState, error) {
	state := UpstreamAccountState{ID: accountStateID(poolID, subject, scope.Operation, scope.Model), PoolID: poolID, Subject: subject, Operation: scope.Operation, Model: scope.Model}
	err := tx.Where("id = ?", state.ID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return state, nil
	}
	return state, err
}

func accountStateID(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(digest, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func safeAccountID(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '-' || r == '_' || r == '.')
	}) < 0
}
func validateAccountRef(ref UpstreamAccountRef) error {
	if !safeAccountID(ref.PoolID) || len(ref.MemberID) != 36 || (ref.QuotaGroup != "" && !safeAccountID(ref.QuotaGroup)) || ref.ChannelID <= 0 || ref.KeyIndex < 0 {
		return errors.New("invalid upstream account reference")
	}
	return nil
}
func validateAccountScope(scope UpstreamAccountScope) error {
	if len(scope.Operation) > 128 || len(scope.Model) > 512 || (scope.QuotaGroup != "" && !safeAccountID(scope.QuotaGroup)) {
		return errors.New("invalid upstream account scope")
	}
	return nil
}
func accountScopeSubject(ref UpstreamAccountRef, scope UpstreamAccountScope) (string, error) {
	if scope.QuotaGroup != "" {
		if scope.QuotaGroup != ref.QuotaGroup {
			return "", errors.New("upstream quota group mismatch")
		}
		return "quota:" + scope.QuotaGroup, nil
	}
	return "member:" + ref.MemberID, nil
}

// SetUpstreamAccountQuotaGroup is an administrator-only membership mutation.
// Empty clears the membership. Selection reads this stored authority under the
// pool coordinator; resolving a credential never overwrites existing membership.
func SetUpstreamAccountQuotaGroup(ctx context.Context, db *gorm.DB, ref UpstreamAccountRef, group string, now time.Time) error {
	if err := validateAccountRef(ref); err != nil {
		return err
	}
	if group != "" && !safeAccountID(group) {
		return errors.New("invalid upstream quota group")
	}
	return accountPoolTransaction(ctx, db, ref.PoolID, nil, func(tx *gorm.DB, _ int64) error {
		result := tx.Model(&UpstreamAccountMember{}).Where("pool_id = ? AND member_id = ?", ref.PoolID, ref.MemberID).Updates(map[string]interface{}{"quota_group": group, "membership_updated_at": now.UnixNano()})
		if result.Error != nil {
			return result.Error
		}
		// MySQL may report zero changed rows for an idempotent membership update.
		var count int64
		if err := tx.Model(&UpstreamAccountMember{}).Where("pool_id = ? AND member_id = ?", ref.PoolID, ref.MemberID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return errors.New("unknown upstream account member")
		}
		return nil
	})
}
