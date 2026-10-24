package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Each fixture owns only its unique rows. Environment DSNs must be disposable.
func TestOCRConfiguredDatabases(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"sqlite", "", sqlite.Open}, {"mysql57", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open}, {"postgres96", "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " not configured")
			}
			if tc.env == "" {
				dsn = filepath.Join(t.TempDir(), "integration.db") + "?_pragma=busy_timeout(5000)"
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			if tc.name == "sqlite" {
				sqlDB.SetMaxOpenConns(1)
			}
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}))
			for i := 0; i < 2; i++ {
				require.NoError(t, db.AutoMigrate(&UserSubscription{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}, &OCRResource{}, &OCRResourceGrant{}))
			}
			user := User{Username: "ocr-" + uuid.NewString()[:12], AffCode: uuid.NewString()[:16], Quota: 1000, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			token := Token{UserId: user.Id, Key: uuid.NewString(), RemainQuota: 500, Status: common.TokenStatusEnabled, ExpiredTime: -1}
			require.NoError(t, db.Create(&token).Error)
			channel := Channel{Name: "ocr-" + uuid.NewString()}
			require.NoError(t, db.Omit("ChannelInfo").Create(&channel).Error)
			require.NoError(t, db.Model(&channel).Update("channel_info", "{}").Error)
			plan := SubscriptionPlan{Title: "ocr-" + uuid.NewString(), QuotaResetPeriod: SubscriptionResetNever}
			require.NoError(t, db.Create(&plan).Error)
			sub := UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1000, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, db.Create(&sub).Error)
			t.Cleanup(func() {
				var ids []string
				require.NoError(t, db.Model(&OCRJob{}).Where("user_id = ?", user.Id).Pluck("id", &ids).Error)
				if len(ids) > 0 {
					require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCROperation{}).Error)
					require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCRAuditEvent{}).Error)
					require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCRResource{}).Error)
					require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCRResourceGrant{}).Error)
					require.NoError(t, db.Where("id IN ?", ids).Delete(&OCRJob{}).Error)
				}
				require.NoError(t, db.Delete(&sub).Error)
				require.NoError(t, db.Delete(&plan).Error)
				require.NoError(t, db.Unscoped().Delete(&token).Error)
				require.NoError(t, db.Unscoped().Delete(&user).Error)
				require.NoError(t, db.Delete(&channel).Error)
			})
			s := NewOCRBillingStore(db, nil)
			ctx := context.Background()
			a := OCRAdmission{IdempotencyKey: uuid.NewString(), UserID: user.Id, TokenID: token.Id, ChannelID: channel.Id, Model: "fixture-ocr", Group: "default", Preference: "wallet_only", Price: 0.0002, GroupRatio: 1, QuotaPerUnit: 500000}
			start := make(chan struct{})
			jobs := make([]OCRJob, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range jobs {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; jobs[i], errs[i] = s.CreateIntent(ctx, a) }(i)
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			assert.Equal(t, jobs[0].ID, jobs[1].ID)
			id := jobs[0].ID
			start = make(chan struct{})
			for i := range errs {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; _, errs[i] = s.Reserve(ctx, id) }(i)
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			ocrBalances(t, s, a, 900, 400, 100)
			start = make(chan struct{})
			claims := make([]OCRClaim, 2)
			for i := range errs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					claims[i], errs[i] = s.ClaimSubmission(ctx, id, ocrTestSelection(a.ChannelID))
				}(i)
			}
			close(start)
			wg.Wait()
			winner := -1
			for i, err := range errs {
				if err == nil {
					require.Equal(t, -1, winner)
					winner = i
				} else {
					require.ErrorIs(t, err, ErrOCRConflict)
				}
			}
			require.NotEqual(t, -1, winner)
			require.NoError(t, s.RecordSubmission(ctx, claims[winner], "provider", false))
			start = make(chan struct{})
			for i := range errs {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; _, errs[i] = s.Finish(ctx, id, "success") }(i)
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			ocrBalances(t, s, a, 900, 400, 100)
			a.IdempotencyKey = uuid.NewString()
			a.Preference = "subscription_only"
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			_, err = s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				require.NoError(t, lockForUpdate(tx).First(&sub, sub.Id).Error)
				return resetUserSubscriptionTx(tx, &sub, &plan, time.Now().Unix(), false)
			}))
			require.NoError(t, db.Model(&sub).Update("amount_used", 37).Error)
			_, err = s.Finish(ctx, j.ID, "cancel")
			require.NoError(t, err)
			require.NoError(t, db.First(&sub, sub.Id).Error)
			assert.EqualValues(t, 37, sub.AmountUsed)
			ocrBalances(t, s, a, 900, 400, 100)
			var events int64
			require.NoError(t, db.Model(&OCRAuditEvent{}).Where("job_id IN ?", []string{id, j.ID}).Count(&events).Error)
			assert.EqualValues(t, 2, events)
			// New durable execution, refusal rearm and resource CAS on every dialect.
			a.IdempotencyKey = uuid.NewString()
			a.Preference = "wallet_only"
			owned, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			require.NoError(t, s.ConfigureOCRSubmission(ctx, owned.ID, OCRExecution{JobsURL: "https://supplier.example/api/v2/ocr/jobs", Model: a.Model, InputDigest: "fixture", MaxAttempts: 2}))
			_, err = s.Reserve(ctx, owned.ID)
			require.NoError(t, err)
			first, err := s.ClaimSubmission(ctx, owned.ID, ocrTestSelection(channel.Id))
			require.NoError(t, err)
			require.NoError(t, s.RecordOCRRefusal(ctx, first, 429, true))
			nextSelection := ocrTestSelection(channel.Id)
			nextSelection.Ref.MemberID = uuid.NewString()
			second, err := s.ClaimSubmission(ctx, owned.ID, nextSelection)
			require.NoError(t, err)
			require.ErrorIs(t, s.RecordSubmission(ctx, first, "late", false), ErrOCRConflict)
			require.NoError(t, s.RecordSubmission(ctx, second, "accepted", false))
			require.NoError(t, s.RecordOCRProgress(ctx, owned.ID, second.ClaimID, "done", "https://supplier.example/result", 1, 1))
			_, err = s.Finish(ctx, owned.ID, "success")
			require.NoError(t, err)
			winners := make([]bool, 2)
			for i := range winners {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					r := OCRResource{ID: uuid.NewString(), JobID: owned.ID, Kind: "jsonl", ObjectKey: "fixture", ContentType: "application/x-ndjson", Size: 10, ExpiresAt: time.Now().Add(time.Hour).Unix()}
					winners[i], errs[i] = s.PublishOCRResources(ctx, owned.ID, []OCRResource{r}, r.ID)
				}(i)
			}
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			assert.NotEqual(t, winners[0], winners[1])
			var resources int64
			require.NoError(t, db.Model(&OCRResource{}).Where("job_id = ?", owned.ID).Count(&resources).Error)
			assert.EqualValues(t, 1, resources)
			current, err := s.Inspect(ctx, owned.ID)
			require.NoError(t, err)
			grant := OCRResourceGrant{ResourceID: current.Job.ResultResourceID, JobID: owned.ID, ExpiresAt: time.Now().Add(time.Hour).Unix(), Digest: strings.Repeat("a", 64)}
			for i := 0; i < 2; i++ {
				require.NoError(t, s.PutOCRGrant(ctx, grant, time.Now().Unix()))
			}
			require.NoError(t, s.ValidateOCRGrant(ctx, owned.ID, grant.ResourceID, grant.Digest, time.Now().Unix()))
			require.ErrorIs(t, s.ValidateOCRGrant(ctx, owned.ID, grant.ResourceID, grant.Digest, grant.ExpiresAt), ErrOCRGrant)
		})
	}
}
