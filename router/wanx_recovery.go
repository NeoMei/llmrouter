package router

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The lease is separate from a call's financial journal. A pass has a bounded
// deadline shorter than the lease, and advances a durable cursor even when a
// supplier remains unavailable, preventing one stuck call starving others.
type wanRecoveryLease struct {
	ID         string `gorm:"primaryKey;size:128"`
	Owner      string `gorm:"size:64"`
	Cursor     string `gorm:"size:128"`
	LeaseUntil int64  `gorm:"index"`
}

const wanRecoveryBatch = 2
const wanRecoveryLeaseDuration = 95 * time.Second
const wanRecoveryPassTimeout = 75 * time.Second
const wanRecoveryCallTimeout = 20 * time.Second

func runWanRecoveryPass(ctx context.Context, db *gorm.DB, instance, owner string, poll func(context.Context, string) error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	now := time.Now().UnixMilli()
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&wanRecoveryLease{ID: instance}).Error; err != nil {
		return false, err
	}
	claim := db.WithContext(ctx).Model(&wanRecoveryLease{}).Where("id = ? AND lease_until < ?", instance, now).Updates(map[string]any{"owner": owner, "lease_until": now + wanRecoveryLeaseDuration.Milliseconds()})
	if claim.Error != nil {
		return false, claim.Error
	}
	if claim.RowsAffected != 1 {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = db.WithContext(cleanup).Model(&wanRecoveryLease{}).Where("id = ? AND owner = ?", instance, owner).Updates(map[string]any{"owner": "", "lease_until": 0}).Error
	}()
	var lease wanRecoveryLease
	if err := db.WithContext(ctx).First(&lease, "id = ? AND owner = ?", instance, owner).Error; err != nil {
		return true, err
	}
	var rows []platformbilling.Call
	query := db.WithContext(ctx).Select("call_id").Where("operation = ? AND wire_operation = ? AND provider_task_id <> ? AND status IN ? AND evidence_conflict = ? AND call_id > ?", "image_edit", "images.edits.create", "", []string{"UNKNOWN", "SETTLE_PENDING"}, false, lease.Cursor)
	if instance == "default" {
		query = query.Where("instance_id IN ?", []string{"", "default"})
	} else {
		query = query.Where("instance_id = ?", instance)
	}
	if err := query.Order("call_id ASC").Limit(wanRecoveryBatch).Find(&rows).Error; err != nil {
		return true, err
	}
	cursor := ""
	for _, row := range rows {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		callCtx, cancel := context.WithTimeout(ctx, wanRecoveryCallTimeout)
		_ = poll(callCtx, row.CallID)
		cancel()
		cursor = row.CallID
	}
	if len(rows) < wanRecoveryBatch {
		cursor = ""
	}
	saved := db.WithContext(ctx).Model(&wanRecoveryLease{}).Where("id = ? AND owner = ? AND lease_until > ?", instance, owner, time.Now().UnixMilli()).Update("cursor", cursor)
	if saved.Error != nil {
		return true, saved.Error
	}
	if saved.RowsAffected != 1 {
		// MySQL may report zero affected rows when the cursor is already empty.
		// Verify the lease instead of treating an unchanged value as lease loss.
		var held int64
		if err := db.WithContext(ctx).Model(&wanRecoveryLease{}).Where("id = ? AND owner = ? AND lease_until > ?", instance, owner, time.Now().UnixMilli()).Where(map[string]any{"cursor": cursor}).Count(&held).Error; err != nil {
			return true, err
		}
		if held != 1 {
			return true, platformbilling.ErrConflict
		}
	}
	return true, nil
}

type discardWanResponse struct{ header http.Header }

func (w *discardWanResponse) Header() http.Header       { return w.header }
func (*discardWanResponse) WriteHeader(int)             {}
func (*discardWanResponse) Write(b []byte) (int, error) { return len(b), nil }

func recoverWanImage(ctx context.Context, a *platformbilling.Adapter, id string) error {
	row, policy, err := a.WanImageTaskForRecovery(ctx, id)
	if err != nil {
		return err
	}
	response := &discardWanResponse{header: make(http.Header)}
	c, _ := gin.CreateTestContext(response)
	c.Request, err = http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)
	if err != nil {
		return err
	}
	pollWanImage(c, a, row, policy)
	return nil
}

// StartWanImageRecovery stays disabled until database-time fencing covers
// every user and worker poll through evidence persistence and settlement.
// No lease migration or worker is started by production in this state.
func StartWanImageRecovery(context.Context, *platformbilling.Adapter, *gorm.DB, string) (func(), error) {
	return func() {}, nil
}

// startUnfencedWanImageRecovery is retained for development only. It must not
// be wired to production until cross-node fencing and interactive poll
// serialization are implemented and verified on every supported database.
func startUnfencedWanImageRecovery(ctx context.Context, a *platformbilling.Adapter, db *gorm.DB, instance string) (func(), error) {
	if !a.Enabled() {
		return func() {}, nil
	}
	if instance == "" {
		instance = "default"
	}
	if err := db.AutoMigrate(&wanRecoveryLease{}); err != nil {
		return nil, err
	}
	worker, cancel := context.WithCancel(ctx)
	owner := uuid.NewString()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			pass, stop := context.WithTimeout(worker, wanRecoveryPassTimeout)
			_, err := runWanRecoveryPass(pass, db, instance, owner, func(callCtx context.Context, id string) error { return recoverWanImage(callCtx, a, id) })
			stop()
			if err != nil && worker.Err() == nil {
				common.SysError("Wan image recovery pass failed")
			}
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); wg.Wait() }, nil
}
