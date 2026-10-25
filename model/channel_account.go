package model

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// ChannelAccountRefs exposes only enabled, channel-authorized members. Raw
// credentials stay in the caller's existing credential store.
func ChannelAccountRefs(ctx context.Context, db *gorm.DB, channel *Channel) ([]UpstreamAccountRef, []string, error) {
	keys := channel.GetKeys()
	refs := make([]UpstreamAccountRef, 0, len(keys))
	for index, key := range keys {
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
			continue
		}
		if key == "" {
			continue
		}
		ref, err := ResolveUpstreamAccountRef(ctx, db, fmt.Sprintf("channel:%d", channel.Id), channel.Id, index, key, "")
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, ref)
	}
	if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeRandom {
		rand.Shuffle(len(refs), func(i, j int) { refs[i], refs[j] = refs[j], refs[i] })
	}
	return refs, keys, nil
}

// ResolveTaskAccountKey never substitutes another account for an owned task.
// Native credentials have no separate provider identity: changing the secret
// creates a new identity and therefore fails closed for old tasks.
func ResolveTaskAccountKey(ctx context.Context, db *gorm.DB, channel *Channel, ref UpstreamAccountRef) (string, error) {
	if ref.ChannelID != channel.Id || ref.PoolID != fmt.Sprintf("channel:%d", channel.Id) {
		return "", fmt.Errorf("task account does not belong to channel")
	}
	refs, keys, err := ChannelAccountRefs(ctx, db, channel)
	if err != nil {
		return "", err
	}
	for _, current := range refs {
		if current.MemberID == ref.MemberID {
			return keys[current.KeyIndex], nil
		}
	}
	return "", fmt.Errorf("task account is unavailable")
}

func TaskAccountKey(ctx context.Context, db *gorm.DB, channel *Channel, task *Task) (string, error) {
	if task.PrivateData.Account != nil {
		return ResolveTaskAccountKey(ctx, db, channel, *task.PrivateData.Account)
	}
	if task.PrivateData.Key != "" {
		return task.PrivateData.Key, nil
	}
	if channel.ChannelInfo.IsMultiKey {
		return "", fmt.Errorf("legacy pooled task has no account owner")
	}
	return channel.Key, nil
}
