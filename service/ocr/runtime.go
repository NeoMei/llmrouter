package ocr

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	"github.com/QuantumNous/new-api/service/objectstore"
	"gorm.io/gorm"
)

// Runtime owns the dedicated billing connection and a cancelable, bounded worker.
// Tests inject NewService and temporary object storage instead of Default().
type Runtime struct {
	DB              *gorm.DB
	Billing         *Billing
	Service         *Service
	mu              sync.Mutex
	started, closed bool
	closeOnce       sync.Once
	closeErr        error
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

func RuntimeFromEnv(db *gorm.DB) (*Runtime, error) {
	origin, secret := os.Getenv("OCR_PUBLIC_ORIGIN"), os.Getenv("OCR_GRANT_SECRET")
	if origin == "" && secret == "" {
		return nil, nil
	}
	if origin == "" || len(secret) < 32 {
		return nil, errors.New("OCR_PUBLIC_ORIGIN and independent OCR_GRANT_SECRET (at least 32 bytes) required")
	}
	if _, err := validateOrigin(origin); err != nil {
		return nil, err
	}
	objects, err := objectstore.Default()
	if err != nil {
		return nil, err
	}
	billing, err := NewBilling(db)
	if err != nil {
		return nil, err
	}
	svc, err := NewService(db, billing, Config{PublicOrigin: origin, GrantSecret: []byte(secret), Objects: objects, GatewayKeys: gateway.GlobalKeySel})
	if err != nil {
		_ = billing.Close()
		return nil, err
	}
	r := &Runtime{DB: db, Billing: billing, Service: svc}
	r.Start()
	return r, nil
}

func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.closed {
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			err := r.RunAuditBatch(ctx, 32)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, model.ErrOCRLogUnsupported) {
				common.SysError(model.ErrOCRLogUnsupported.Error())
			} else if err != nil {
				common.SysError("OCR consumption-log projection pending: database transaction or lookup failed; audit retained for retry")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		cursor := ""
		for {
			next, err := r.RunBatch(ctx, cursor, 32)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				common.SysError("OCR background recovery batch failed")
				cursor = ""
			} else {
				cursor = next
			}
			purgeCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			if err := r.Service.PurgeExpiredResources(purgeCtx, 100); err != nil && ctx.Err() == nil {
				common.SysError("OCR resource purge failed")
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Unlock()
		r.wg.Wait()
		r.closeErr = r.Billing.Close()
	})
	return r.closeErr
}

// RunBatch scans only known accepted jobs and durable terminal refund decisions.
// A cursor keeps repeatedly failing old jobs from starving newer jobs. No create
// is ever retried by this worker. Service/model CAS owns multi-instance safety.
func (r *Runtime) RunBatch(ctx context.Context, after string, limit int) (string, error) {
	if limit < 1 || limit > 100 {
		return after, errors.New("invalid OCR worker batch size")
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var jobs []model.OCRJob
	err := r.DB.WithContext(ctx).Where("id > ?", after).Where(
		"(submission_state = ? AND provider_job_id <> ? AND result_resource_id = ? AND ((provider_state NOT IN ?) OR (provider_state = ? AND billing_state <> ?) OR (provider_state = ? AND (billing_state <> ? OR created_at > ?)))) OR (decision IN ? AND billing_state <> ?)",
		"submitted", "", "", []string{"done", "failed"}, "failed", "refunded", "done", "settled", r.Service.now().Add(-r.Service.retention).Unix(), []string{"failure", "cancel"}, "refunded").Order("id ASC").Limit(limit).Find(&jobs).Error
	if err != nil {
		return after, err
	}
	for _, j := range jobs {
		if parent.Err() != nil {
			return after, parent.Err()
		}
		if ctx.Err() != nil {
			return after, nil
		}
		jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := r.Service.Status(jobCtx, j.ID, j.UserID, j.TokenID)
		cancel()
		if err != nil && ctx.Err() == nil {
			common.SysError("OCR accepted job recovery pending")
		}
		after = j.ID
	}
	if len(jobs) < limit {
		after = ""
	}
	return after, nil
}

// RunAuditBatch runs independently from supplier polling and never changes its
// outcome. Both workers are canceled and joined before billing/database close.
func (r *Runtime) RunAuditBatch(ctx context.Context, limit int) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.Billing.ProjectAuditLogs(ctx, model.LOG_DB, limit)
}
