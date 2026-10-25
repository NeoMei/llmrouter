package model

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTaskAccountSurvivesReorderButNeverChangesIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))
	ch := &Channel{Id: 21, Key: "appA+tokenA\nappB+tokenB", ChannelInfo: ChannelInfo{IsMultiKey: true}}
	refs, _, err := ChannelAccountRefs(context.Background(), db, ch)
	require.NoError(t, err)
	task := &Task{ChannelId: 21, PrivateData: TaskPrivateData{Account: &refs[0]}}
	ch.Key = "appB+tokenB\nappA+tokenA"
	key, err := TaskAccountKey(context.Background(), db, ch, task)
	require.NoError(t, err)
	assert.Equal(t, "appA+tokenA", key)
	require.NoError(t, SetUpstreamAccountPaused(context.Background(), db, refs[0], true, time.Now()))
	key, err = TaskAccountKey(context.Background(), db, ch, task)
	require.NoError(t, err)
	assert.Equal(t, "appA+tokenA", key, "pause prevents new submissions but must not change task ownership")
	ch.Key = "appB+tokenB\nappA+replaced"
	_, err = TaskAccountKey(context.Background(), db, ch, task)
	require.Error(t, err)
	_, err = TaskAccountKey(context.Background(), db, ch, &Task{})
	require.Error(t, err, "unowned legacy multikey must not become an auth blob")
}
