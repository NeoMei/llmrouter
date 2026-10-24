package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"gorm.io/gorm"
)

type KeySelector struct {
	db     *gorm.DB
	crypto *crypto.AESGCM
}

func NewKeySelector(db *gorm.DB, c *crypto.AESGCM) *KeySelector {
	return &KeySelector{db: db, crypto: c}
}

// PlatformAccounts returns current credential-generation refs and stable owners.
// Administration assigns quota membership to Owner; each generation inherits it.
func (ks *KeySelector) PlatformAccounts(ctx context.Context, profileID, channelID int) ([]model.UpstreamAccountRef, map[int]string, map[int]model.UpstreamAccountRef, error) {
	var rows []model.ProviderKeyPool
	now := time.Now()
	err := ks.db.WithContext(ctx).Where("profile_id = ? AND status = ?", profileID, "active").Order("last_used_at ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, nil, nil, err
	}
	refs := make([]model.UpstreamAccountRef, 0, len(rows))
	var earliest time.Time
	keys, owners := map[int]string{}, map[int]model.UpstreamAccountRef{}
	for _, row := range rows {
		if row.CooldownUntil != nil && row.CooldownUntil.After(now) {
			if earliest.IsZero() || row.CooldownUntil.Before(earliest) {
				earliest = *row.CooldownUntil
			}
			continue
		}
		key, err := decryptKey(ks.crypto, row.EncryptedKey)
		if err != nil {
			return nil, nil, nil, err
		}
		owner, err := model.ResolveUpstreamAccountRef(ctx, ks.db, fmt.Sprintf("gateway:%d", profileID), channelID, row.Id, fmt.Sprintf("provider-key:%d", row.Id), "")
		if err != nil {
			return nil, nil, nil, err
		}
		if _, err := model.SelectUpstreamAccount(ctx, ks.db, []model.UpstreamAccountRef{owner}, model.UpstreamAccountScope{}, nil, now); err != nil {
			var unavailable *model.UpstreamAccountUnavailable
			if !errors.As(err, &unavailable) {
				return nil, nil, nil, err
			}
			if !unavailable.RetryAt.IsZero() && (earliest.IsZero() || unavailable.RetryAt.Before(earliest)) {
				earliest = unavailable.RetryAt
			}
			continue
		}
		ref, err := model.ResolveUpstreamAccountRef(ctx, ks.db, owner.PoolID, channelID, row.Id, fmt.Sprintf("provider-key:%d:credential:%s", row.Id, key), owner.QuotaGroup)
		if err != nil {
			return nil, nil, nil, err
		}
		if ref.QuotaGroup != owner.QuotaGroup {
			if err := model.SetUpstreamAccountQuotaGroup(ctx, ks.db, ref, owner.QuotaGroup, now); err != nil {
				return nil, nil, nil, err
			}
			ref.QuotaGroup = owner.QuotaGroup
		}
		refs, keys[row.Id], owners[row.Id] = append(refs, ref), key, owner
	}
	if len(refs) == 0 {
		return nil, nil, nil, &model.UpstreamAccountUnavailable{RetryAt: earliest}
	}
	return refs, keys, owners, nil
}

func (ks *KeySelector) SelectPlatformAccount(ctx context.Context, profileID, channelID int, scope model.UpstreamAccountScope, excluded map[string]bool) (model.UpstreamAccountSelection, string, error) {
	refs, keys, _, err := ks.PlatformAccounts(ctx, profileID, channelID)
	if err != nil {
		return model.UpstreamAccountSelection{}, "", err
	}
	now := time.Now()
	selected, err := model.SelectUpstreamAccount(ctx, ks.db, refs, scope, excluded, now)
	if err != nil {
		return selected, "", err
	}
	err = ks.db.WithContext(ctx).Model(&model.ProviderKeyPool{}).Where("id = ? AND profile_id = ?", selected.KeyIndex, profileID).Update("last_used_at", now).Error
	return selected, keys[selected.KeyIndex], err
}

func (ks *KeySelector) SelectPlatformKey(ctx context.Context, profileID int) (int, string, error) {
	selected, key, err := ks.SelectPlatformAccount(ctx, profileID, profileID, model.UpstreamAccountScope{}, nil)
	return selected.KeyIndex, key, err
}

// An existing but disabled or unreadable BYOK record is a refusal, never an
// invitation to spend a platform account's quota.
func (ks *KeySelector) GetBYOKKey(ctx context.Context, userID, profileID int) (string, error) {
	var row model.UserProviderKey
	err := ks.db.WithContext(ctx).Where("user_id = ? AND profile_id = ?", userID, profileID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.Status != "active" {
		return "", errors.New("BYOK account unavailable")
	}
	return decryptKey(ks.crypto, row.EncryptedKey)
}

func decryptKey(c *crypto.AESGCM, encrypted []byte) (string, error) {
	if c == nil {
		return "", errors.New("credential decryption unavailable")
	}
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		return "", errors.New("failed to decrypt account credential")
	}
	return string(decrypted), nil
}

// ResolveAccountKey resolves a stable platform-row identity after credential
// refresh, without allowing another row or profile to inherit task ownership.
func (ks *KeySelector) ResolveAccountKey(ctx context.Context, ref model.UpstreamAccountRef) (string, error) {
	var row model.ProviderKeyPool
	if err := ks.db.WithContext(ctx).Where("id = ? AND status = ?", ref.KeyIndex, "active").First(&row).Error; err != nil {
		return "", err
	}
	if ref.PoolID != fmt.Sprintf("gateway:%d", row.ProfileId) {
		return "", errors.New("account profile mismatch")
	}
	current, err := model.ResolveUpstreamAccountRef(ctx, ks.db, ref.PoolID, ref.ChannelID, row.Id, fmt.Sprintf("provider-key:%d", row.Id), "")
	if err != nil {
		return "", err
	}
	if current.MemberID != ref.MemberID {
		return "", errors.New("account identity mismatch")
	}
	return decryptKey(ks.crypto, row.EncryptedKey)
}

// ResolvePlatformOwner returns the stable management handle even while the
// provider row is disabled or its current credential is cooling down. Admin
// pause/resume and quota membership belong to this handle, not to a generation.
func (ks *KeySelector) ResolvePlatformOwner(ctx context.Context, profileID, channelID, keyID int) (model.UpstreamAccountRef, error) {
	var row model.ProviderKeyPool
	if err := ks.db.WithContext(ctx).Where("id = ? AND profile_id = ?", keyID, profileID).First(&row).Error; err != nil {
		return model.UpstreamAccountRef{}, err
	}
	return model.ResolveUpstreamAccountRef(ctx, ks.db, fmt.Sprintf("gateway:%d", profileID), channelID, keyID, fmt.Sprintf("provider-key:%d", keyID), "")
}
