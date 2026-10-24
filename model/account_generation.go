package model

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// SyncUpstreamAccountGeneration inherits membership from the CURRENT stored
// stable owner, not a caller's earlier snapshot. The owning pool coordinator
// remains locked from that read through generation resolution/update, so an
// administrator cannot commit a new membership between those operations.
// Credential identity remains private and is supplied by its existing store.
// No credential-store row or other pool is acquired while holding this lock.
func SyncUpstreamAccountGeneration(ctx context.Context, db *gorm.DB, owner UpstreamAccountRef, identity string, now time.Time) (UpstreamAccountRef, error) {
	if err := validateAccountRef(owner); err != nil {
		return UpstreamAccountRef{}, err
	}
	var generation UpstreamAccountRef
	err := accountPoolTransaction(ctx, db, owner.PoolID, nil, func(tx *gorm.DB, _ int64) error {
		current, err := CurrentManagedAccountRef(ctx, tx, owner)
		if err != nil {
			return err
		}
		generation, err = ResolveUpstreamAccountRef(ctx, tx, current.PoolID, current.ChannelID, current.KeyIndex, identity, current.QuotaGroup)
		if err != nil {
			return err
		}
		// Resolve can be called within an older MySQL MVCC snapshot. Its returned
		// membership is not authority; use a current locking read under this pool.
		generation, err = CurrentManagedAccountRef(ctx, tx, generation)
		if err != nil {
			return err
		}
		if generation.QuotaGroup != current.QuotaGroup {
			if err = SetUpstreamAccountQuotaGroup(ctx, tx, generation, current.QuotaGroup, now); err != nil {
				return err
			}
			generation.QuotaGroup = current.QuotaGroup
		}
		return nil
	})
	return generation, err
}
