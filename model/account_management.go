package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrAccountNotPool = errors.New("not_pool")

// AccountBlocker contains only server-allowlisted health information. It never
// carries a provider error, credential identity, or persisted state namespace.
type AccountBlocker struct {
	Reason    string     `json:"reason"`
	Scope     string     `json:"scope"`
	Operation string     `json:"operation,omitempty"`
	Model     string     `json:"model,omitempty"`
	RetryAt   *time.Time `json:"retry_at,omitempty"`
}
type AccountHealth struct {
	MemberID   string           `json:"member_id"`
	QuotaGroup string           `json:"quota_group"`
	Paused     bool             `json:"paused"`
	Isolated   bool             `json:"isolated"`
	RetryAt    *time.Time       `json:"retry_at,omitempty"`
	Blockers   []AccountBlocker `json:"blockers"`
}

// AccountManagementMember is an internal safe resolver result; Ref and Health
// are server-owned authority, never accepted from a client.
type AccountManagementMember struct {
	Ref                   UpstreamAccountRef
	Health                UpstreamAccountRef
	Indices               []int
	LegacyDisabledIndices []int
	Barriers              []AccountBlocker
}

// ProjectUpstreamAccounts uses the selector's pool -> sorted groups lock order
// and current stored membership, without advancing any member's rotation.
func ProjectUpstreamAccounts(ctx context.Context, db *gorm.DB, refs []UpstreamAccountRef, scope UpstreamAccountScope, now time.Time) ([]AccountHealth, error) {
	result := make([]AccountHealth, 0, len(refs))
	if err := validateAccountScope(scope); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return result, nil
	}
	pool := refs[0].PoolID
	err := accountPoolTransaction(ctx, db, pool, nil, func(tx *gorm.DB, _ int64) error {
		ids := make([]string, 0, len(refs))
		for _, ref := range refs {
			if err := validateAccountRef(ref); err != nil {
				return err
			}
			if ref.PoolID != pool {
				return errors.New("mixed account pools")
			}
			ids = append(ids, ref.MemberID)
		}
		var members []UpstreamAccountMember
		if err := lockForUpdate(tx).Where("pool_id = ? AND member_id IN ?", accountStateID("pool:"+pool), ids).Find(&members).Error; err != nil {
			return err
		}
		groups := make([]string, 0, len(members))
		byID := map[string]UpstreamAccountMember{}
		for _, m := range members {
			byID[m.MemberID] = m
			groups = append(groups, m.QuotaGroup)
		}
		if err := lockAccountQuotaGroups(tx, groups); err != nil {
			return err
		}
		namespaces := []string{accountStateID("pool:" + pool)}
		for _, group := range groups {
			if group != "" {
				namespaces = append(namespaces, accountStateID("quota:"+group))
			}
		}
		var states []UpstreamAccountState
		if err := lockForUpdate(tx).Where("pool_id IN ?", namespaces).Find(&states).Error; err != nil {
			return err
		}
		for _, ref := range refs {
			member, ok := byID[ref.MemberID]
			if !ok {
				return errors.New("unknown account member")
			}
			h := AccountHealth{MemberID: ref.MemberID, QuotaGroup: member.QuotaGroup, Blockers: []AccountBlocker{}}
			for _, state := range states {
				memberMatch := state.PoolID == accountStateID("pool:"+pool) && state.Subject == "member:"+ref.MemberID
				groupMatch := member.QuotaGroup != "" && state.PoolID == accountStateID("quota:"+member.QuotaGroup) && state.Subject == "quota:"+member.QuotaGroup
				if (!memberMatch && !groupMatch) || (state.Operation != "" && state.Operation != scope.Operation) || (state.Model != "" && state.Model != scope.Model) {
					continue
				}
				b := AccountBlocker{Scope: "account", Operation: state.Operation, Model: state.Model}
				if groupMatch {
					b.Scope = "quota_group_global"
				}
				if state.Paused {
					h.Paused = true
					b.Reason = "manual_pause"
					h.Blockers = append(h.Blockers, b)
				}
				if state.Kind == UpstreamAccountInvalid {
					h.Isolated = true
					b.Reason = "invalid_credential"
					h.Blockers = append(h.Blockers, b)
				}
				if state.Kind == UpstreamAccountCooldown && state.RetryAt > now.UnixNano() {
					retry := time.Unix(0, state.RetryAt).UTC()
					b.RetryAt = &retry
					b.Reason = "cooldown"
					if state.Reason == "rate_limit" || state.Reason == "quota_exhausted" {
						b.Reason = state.Reason
					}
					h.Blockers = append(h.Blockers, b)
					if h.RetryAt == nil || retry.After(*h.RetryAt) {
						h.RetryAt = &retry
					}
				}
			}
			if h.Paused || h.Isolated {
				h.RetryAt = nil
			}
			result = append(result, h)
		}
		return nil
	})
	return result, err
}

// ChannelAccountManagement includes disabled keys and deduplicates exact
// credentials; any enabled occurrence makes that account legacy-eligible.
func ChannelAccountManagement(ctx context.Context, db *gorm.DB, channel *Channel) ([]AccountManagementMember, error) {
	if !channel.ChannelInfo.IsMultiKey {
		return nil, ErrAccountNotPool
	}
	if len(channel.GetKeys()) > 2000 {
		return nil, errors.New("pool_too_large")
	}
	rows := []AccountManagementMember{}
	err := AccountManagementTransaction(ctx, db, fmt.Sprintf("channel:%d", channel.Id), func(tx *gorm.DB) error {
		db = tx
		positions := map[string]int{}
		enabled := map[string]bool{}
		for index, key := range channel.GetKeys() {
			if key == "" {
				continue
			}
			ref, err := ResolveUpstreamAccountRef(ctx, db, fmt.Sprintf("channel:%d", channel.Id), channel.Id, index, key, "")
			if err != nil {
				return err
			}
			pos, exists := positions[ref.MemberID]
			if !exists {
				pos = len(rows)
				positions[ref.MemberID] = pos
				rows = append(rows, AccountManagementMember{Ref: ref, Health: ref, Indices: []int{}, LegacyDisabledIndices: []int{}, Barriers: []AccountBlocker{}})
			}
			rows[pos].Indices = append(rows[pos].Indices, index)
			status, explicit := channel.ChannelInfo.MultiKeyStatusList[index]
			if !explicit || status == common.ChannelStatusEnabled {
				enabled[ref.MemberID] = true
			} else {
				rows[pos].LegacyDisabledIndices = append(rows[pos].LegacyDisabledIndices, index)
			}
		}
		for i := range rows {
			if !enabled[rows[i].Ref.MemberID] {
				rows[i].Barriers = append(rows[i].Barriers, AccountBlocker{Reason: "legacy_disabled", Scope: "account"})
			}
		}
		return nil
	})
	return rows, err
}

// WithAccountManagementChannel reads credentials from the current DB row and
// retains its row lock through handle resolution and mutation. SQLite's write
// transaction serializes this with other credential writers.
func WithAccountManagementChannel(ctx context.Context, db *gorm.DB, id int, apply func(*gorm.DB, *Channel) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&Channel{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
				return err
			}
		}
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, "id = ?", id).Error; err != nil {
			return err
		}
		return apply(tx, &channel)
	})
}

// AccountManagementProviderRows must be called within the current channel
// transaction for mutations, before locking the account pool coordinator.
func AccountManagementProviderRows(ctx context.Context, db *gorm.DB, profileID int) ([]ProviderKeyPool, error) {
	var rows []ProviderKeyPool
	err := lockForUpdate(db.WithContext(ctx)).Where("profile_id = ?", profileID).Order("id ASC").Limit(2001).Find(&rows).Error
	if err == nil && len(rows) > 2000 {
		return nil, errors.New("pool_too_large")
	}
	return rows, err
}

// MutateManagedAccount checks current membership under the same pool lock as
// selection. The caller has already re-resolved this handle from a locked
// current credential store. Shared-group actions name the operator's intended
// group explicitly; a stale browser must never recover a replacement group.
func MutateManagedAccount(ctx context.Context, db *gorm.DB, owner, health UpstreamAccountRef, action string, scope UpstreamAccountScope, group, expectedGroup *string, now time.Time) error {
	if err := validateAccountRef(owner); err != nil {
		return err
	}
	if health.MemberID != "" {
		if err := validateAccountRef(health); err != nil {
			return err
		}
		if health.PoolID != owner.PoolID || health.ChannelID != owner.ChannelID || health.KeyIndex != owner.KeyIndex {
			return errors.New("invalid account health owner")
		}
	}
	return accountPoolTransaction(ctx, db, owner.PoolID, nil, func(tx *gorm.DB, _ int64) error {
		var member UpstreamAccountMember
		if err := lockForUpdate(tx).Where("pool_id = ? AND member_id = ?", accountStateID("pool:"+owner.PoolID), owner.MemberID).First(&member).Error; err != nil {
			return err
		}
		owner.QuotaGroup = member.QuotaGroup
		switch action {
		case "pause", "resume":
			return SetUpstreamAccountPaused(ctx, tx, owner, action == "pause", now)
		case "recover":
			return RecoverUpstreamAccount(ctx, tx, health, scope, now)
		case "recover-group":
			if group == nil || *group == "" || *group != member.QuotaGroup {
				return errors.New("stale_quota_group")
			}
			scope.QuotaGroup = *group
			return RecoverUpstreamAccount(ctx, tx, owner, scope, now)
		case "quota-group":
			if group == nil || expectedGroup == nil {
				return errors.New("invalid_request")
			}
			if *expectedGroup != member.QuotaGroup {
				return errors.New("stale_quota_group")
			}
			if err := SetUpstreamAccountQuotaGroup(ctx, tx, owner, *group, now); err != nil {
				return err
			}
			if health.MemberID != "" && health.MemberID != owner.MemberID {
				return SetUpstreamAccountQuotaGroup(ctx, tx, health, *group, now)
			}
			return nil
		}
		return errors.New("invalid_action")
	})
}

// AccountManagementTransaction establishes pool-before-member lock ordering for
// complete management resolvers. An outer credential-store transaction retains
// this coordinator lock until its mutation or snapshot has completed.
func AccountManagementTransaction(ctx context.Context, db *gorm.DB, poolID string, apply func(*gorm.DB) error) error {
	return accountPoolTransaction(ctx, db, poolID, nil, func(tx *gorm.DB, _ int64) error { return apply(tx) })
}

// CurrentManagedAccountRef bypasses any older MySQL REPEATABLE READ snapshot
// after the pool coordinator has been acquired. Membership is current authority.
func CurrentManagedAccountRef(ctx context.Context, db *gorm.DB, ref UpstreamAccountRef) (UpstreamAccountRef, error) {
	if err := validateAccountRef(ref); err != nil {
		return UpstreamAccountRef{}, err
	}
	var member UpstreamAccountMember
	if err := lockForUpdate(db.WithContext(ctx)).Where("pool_id = ? AND member_id = ?", accountStateID("pool:"+ref.PoolID), ref.MemberID).First(&member).Error; err != nil {
		return UpstreamAccountRef{}, err
	}
	ref.QuotaGroup = member.QuotaGroup
	return ref, nil
}

// AccountManagementProfile locks the same first active exact model/profile
// mapping used by Gateway admission. Inactive configuration remains inspectable
// so a management view can explain its barrier without claiming availability.
func AccountManagementProfile(ctx context.Context, db *gorm.DB, modelName string) (ProviderModel, ProviderProfile, error) {
	var pm ProviderModel
	err := lockForUpdate(db.WithContext(ctx)).Where("model_id = ? AND status = ?", modelName, "active").Order("id ASC").First(&pm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = lockForUpdate(db.WithContext(ctx)).Where("model_id = ?", modelName).Order("id ASC").First(&pm).Error
	}
	if err != nil {
		return pm, ProviderProfile{}, err
	}
	if pm.ModelId != modelName {
		return pm, ProviderProfile{}, errors.New("model profile mismatch")
	}
	var pp ProviderProfile
	err = lockForUpdate(db.WithContext(ctx)).First(&pp, "id = ?", pm.ProfileId).Error
	return pm, pp, err
}
