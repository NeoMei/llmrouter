package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// ManagementAccounts never returns decrypted credentials or legacy raw errors.
// Unlike PlatformAccounts it includes inactive, cooled, paused and unreadable
// rows. The supplied transaction is authoritative; the selector's DB is unused.
func (ks *KeySelector) ManagementAccounts(ctx context.Context, db *gorm.DB, profileID, channelID int, now time.Time) ([]model.AccountManagementMember, error) {
	rows, err := model.AccountManagementProviderRows(ctx, db, profileID)
	if err != nil {
		return nil, err
	}
	var result []model.AccountManagementMember
	err = model.AccountManagementTransaction(ctx, db, fmt.Sprintf("gateway:%d", profileID), func(tx *gorm.DB) error {
		db = tx
		result = make([]model.AccountManagementMember, 0, len(rows))
		for index, row := range rows {
			owner, err := model.ResolveUpstreamAccountRef(ctx, db, fmt.Sprintf("gateway:%d", profileID), channelID, row.Id, fmt.Sprintf("provider-key:%d", row.Id), "")
			if err != nil {
				return err
			}
			owner, err = model.CurrentManagedAccountRef(ctx, db, owner)
			if err != nil {
				return err
			}
			item := model.AccountManagementMember{Ref: owner, Indices: []int{index}, Barriers: []model.AccountBlocker{}}
			if row.Status != "active" {
				item.Barriers = append(item.Barriers, model.AccountBlocker{Reason: "legacy_inactive", Scope: "account"})
			}
			if row.CooldownUntil != nil && row.CooldownUntil.After(now) {
				retry := row.CooldownUntil.UTC()
				item.Barriers = append(item.Barriers, model.AccountBlocker{Reason: "legacy_cooldown", Scope: "account", RetryAt: &retry})
			}
			var key string
			if ks == nil || ks.crypto == nil {
				item.Barriers = append(item.Barriers, model.AccountBlocker{Reason: "configuration_unavailable", Scope: "account"})
			} else {
				key, err = decryptKey(ks.crypto, row.EncryptedKey)
				if err != nil || key == "" {
					item.Barriers = append(item.Barriers, model.AccountBlocker{Reason: "credential_unavailable", Scope: "account"})
				}
			}
			if key != "" {
				item.Health, err = model.ResolveUpstreamAccountRef(ctx, db, owner.PoolID, channelID, row.Id, fmt.Sprintf("provider-key:%d:credential:%s", row.Id, key), owner.QuotaGroup)
				if err != nil {
					return err
				}
				if item.Health.QuotaGroup != owner.QuotaGroup {
					if err = model.SetUpstreamAccountQuotaGroup(ctx, db, item.Health, owner.QuotaGroup, now); err != nil {
						return err
					}
					item.Health.QuotaGroup = owner.QuotaGroup
				}
			}
			result = append(result, item)
		}
		return nil
	})
	return result, err
}
