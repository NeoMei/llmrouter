package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	FineTuningStatusQueued    = "queued"
	FineTuningStatusRunning   = "running"
	FineTuningStatusSucceeded = "succeeded"
	FineTuningStatusFailed    = "failed"
	FineTuningStatusCancelled = "cancelled"
)

type FineTuningJob struct {
	ID               string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	TenantID         string `json:"-" gorm:"type:varchar(128);index:idx_fine_tuning_owner,priority:1"`
	UserID           int    `json:"-" gorm:"index:idx_fine_tuning_owner,priority:2"`
	TrainingFileID   string `json:"training_file" gorm:"type:varchar(64);index"`
	ValidationFileID string `json:"validation_file,omitempty" gorm:"type:varchar(64);index"`
	Model            string `json:"model" gorm:"type:varchar(255);index"`
	Suffix           string `json:"suffix,omitempty" gorm:"type:varchar(64)"`
	Hyperparameters  string `json:"-" gorm:"type:text"`
	ChannelID        int    `json:"-" gorm:"index"`
	ProviderType     int    `json:"-" gorm:"index"`
	UpstreamID       string `json:"-" gorm:"type:varchar(255);index"`
	Status           string `json:"status" gorm:"type:varchar(32);index"`
	ResultModel      string `json:"fine_tuned_model,omitempty" gorm:"type:varchar(255);index"`
	FailureJSON      string `json:"-" gorm:"type:text"`
	BillingSnapshot  string `json:"-" gorm:"type:text"`
	BillingSettledAt int64  `json:"-" gorm:"index"`
	PollLockedBy     string `json:"-" gorm:"type:varchar(128);index"`
	PollLeaseUntil   int64  `json:"-" gorm:"index"`
	PollAfter        int64  `json:"-" gorm:"index"`
	PollAttempts     int    `json:"-"`
	Version          int64  `json:"-"`
	CreatedAt        int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        int64  `json:"updated_at" gorm:"autoUpdateTime"`
	FinishedAt       int64  `json:"finished_at,omitempty"`
	CancelledAt      int64  `json:"cancelled_at,omitempty"`
	ModelDeletedAt   int64  `json:"-" gorm:"index"`
}

type FineTuningEvent struct {
	ID              int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	JobID           string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_fine_tuning_event_sequence,priority:1;uniqueIndex:idx_fine_tuning_event_upstream,priority:1"`
	Sequence        int64  `json:"-" gorm:"uniqueIndex:idx_fine_tuning_event_sequence,priority:2"`
	UpstreamEventID string `json:"-" gorm:"type:varchar(255);uniqueIndex:idx_fine_tuning_event_upstream,priority:2"`
	Level           string `json:"level" gorm:"type:varchar(32)"`
	Message         string `json:"message" gorm:"type:text"`
	DataJSON        string `json:"-" gorm:"type:text"`
	CreatedAt       int64  `json:"created_at" gorm:"index"`
}

func CreateFineTuningJob(job *FineTuningJob) error {
	if job.ID == "" {
		job.ID = "ftjob-" + uuid.NewString()
	}
	if job.Status == "" {
		job.Status = FineTuningStatusQueued
	}
	return DB.Create(job).Error
}

func GetOwnedFineTuningJob(tenantID string, userID int, id string) (*FineTuningJob, error) {
	var job FineTuningJob
	if err := DB.Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func GetFineTuningJobByID(id string) (*FineTuningJob, error) {
	var job FineTuningJob
	if err := DB.Where("id = ?", id).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func ListFineTuningJobs(tenantID string, userID int, after string, limit int) ([]FineTuningJob, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := DB.Where("tenant_id = ? AND user_id = ?", tenantID, userID)
	if after != "" {
		query = query.Where("id < ?", after)
	}
	var jobs []FineTuningJob
	return jobs, query.Order("id DESC").Limit(limit).Find(&jobs).Error
}

func GetOwnedCompletedFineTuningJobByModel(tenantID string, userID int, modelID string) (*FineTuningJob, error) {
	var job FineTuningJob
	err := DB.Where("tenant_id = ? AND user_id = ? AND result_model = ? AND status = ? AND model_deleted_at = 0",
		tenantID, userID, modelID, FineTuningStatusSucceeded).Order("id DESC").First(&job).Error
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func MarkFineTunedModelDeleted(id string) (bool, error) {
	result := DB.Model(&FineTuningJob{}).Where("id = ? AND status = ? AND model_deleted_at = 0", id, FineTuningStatusSucceeded).
		Updates(map[string]any{"model_deleted_at": common.GetTimestamp(), "updated_at": common.GetTimestamp()})
	return result.RowsAffected == 1, result.Error
}

func TransitionFineTuningJob(id string, from []string, to string, updates map[string]any) (bool, error) {
	if len(from) == 0 || to == "" {
		return false, errors.New("fine-tuning transition requires source and destination states")
	}
	values := make(map[string]any, len(updates)+3)
	for key, value := range updates {
		values[key] = value
	}
	values["status"] = to
	values["version"] = gorm.Expr("version + 1")
	values["updated_at"] = time.Now().Unix()
	result := DB.Model(&FineTuningJob{}).Where("id = ? AND status IN ?", id, from).Updates(values)
	return result.RowsAffected == 1, result.Error
}

func AppendFineTuningEvent(event *FineTuningEvent) (*FineTuningEvent, bool, error) {
	if event.JobID == "" {
		return nil, false, errors.New("fine-tuning event job id is required")
	}
	if event.UpstreamEventID == "" {
		event.UpstreamEventID = "event-" + uuid.NewString()
	}
	var created bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing FineTuningEvent
		err := tx.Where("job_id = ? AND upstream_event_id = ?", event.JobID, event.UpstreamEventID).First(&existing).Error
		if err == nil {
			*event = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var maxSequence int64
		if err := tx.Model(&FineTuningEvent{}).Where("job_id = ?", event.JobID).Select("COALESCE(MAX(sequence), 0)").Scan(&maxSequence).Error; err != nil {
			return err
		}
		event.Sequence = maxSequence + 1
		if event.CreatedAt == 0 {
			event.CreatedAt = common.GetTimestamp()
		}
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return event, created, err
}

func ListFineTuningEvents(tenantID string, userID int, jobID string, afterSequence int64, limit int) ([]FineTuningEvent, error) {
	if _, err := GetOwnedFineTuningJob(tenantID, userID, jobID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	var events []FineTuningEvent
	err := DB.Where("job_id = ? AND sequence > ?", jobID, afterSequence).Order("sequence ASC, id ASC").Limit(limit).Find(&events).Error
	return events, err
}

func ListPollableFineTuningJobs(now int64, limit int) ([]FineTuningJob, error) {
	if limit <= 0 {
		limit = 20
	}
	var jobs []FineTuningJob
	err := DB.Where("status IN ? AND poll_after <= ? AND poll_lease_until < ?", []string{FineTuningStatusQueued, FineTuningStatusRunning}, now, now).
		Order("poll_after ASC, id ASC").Limit(limit).Find(&jobs).Error
	return jobs, err
}

func HasPollableFineTuningJobs() bool {
	var count int64
	now := common.GetTimestamp()
	err := DB.Model(&FineTuningJob{}).
		Where("status IN ? AND poll_after <= ? AND poll_lease_until < ?", []string{FineTuningStatusQueued, FineTuningStatusRunning}, now, now).
		Limit(1).Count(&count).Error
	return err == nil && count > 0
}

func ClaimFineTuningJob(id, runnerID string, now, leaseUntil int64) (bool, error) {
	result := DB.Model(&FineTuningJob{}).
		Where("id = ? AND status IN ? AND poll_after <= ? AND poll_lease_until < ?", id, []string{FineTuningStatusQueued, FineTuningStatusRunning}, now, now).
		Updates(map[string]any{"poll_locked_by": runnerID, "poll_lease_until": leaseUntil, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func CompleteFineTuningPoll(id, runnerID, status, resultModel, failureJSON string, events []FineTuningEvent, nextPollAt int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var job FineTuningJob
		if err := tx.Where("id = ? AND poll_locked_by = ?", id, runnerID).First(&job).Error; err != nil {
			return err
		}
		for index := range events {
			events[index].JobID = id
			var existing FineTuningEvent
			err := tx.Where("job_id = ? AND upstream_event_id = ?", id, events[index].UpstreamEventID).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var maxSequence int64
			if err := tx.Model(&FineTuningEvent{}).Where("job_id = ?", id).Select("COALESCE(MAX(sequence), 0)").Scan(&maxSequence).Error; err != nil {
				return err
			}
			events[index].Sequence = maxSequence + 1
			if events[index].CreatedAt == 0 {
				events[index].CreatedAt = common.GetTimestamp()
			}
			if events[index].UpstreamEventID == "" {
				events[index].UpstreamEventID = "event-" + uuid.NewString()
			}
			if err := tx.Create(&events[index]).Error; err != nil {
				return err
			}
		}
		now := common.GetTimestamp()
		updates := map[string]any{
			"status": status, "result_model": resultModel, "failure_json": failureJSON,
			"poll_locked_by": "", "poll_lease_until": 0, "poll_after": nextPollAt,
			"poll_attempts": 0, "version": gorm.Expr("version + 1"), "updated_at": now,
		}
		if IsFineTuningTerminal(status) {
			updates["finished_at"] = now
		}
		result := tx.Model(&FineTuningJob{}).Where("id = ? AND poll_locked_by = ?", id, runnerID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("fine-tuning poll lease lost")
		}
		return nil
	})
}

func FailFineTuningPoll(id, runnerID string, nextPollAt int64) error {
	result := DB.Model(&FineTuningJob{}).Where("id = ? AND poll_locked_by = ?", id, runnerID).Updates(map[string]any{
		"poll_locked_by": "", "poll_lease_until": 0, "poll_after": nextPollAt,
		"poll_attempts": gorm.Expr("poll_attempts + 1"), "updated_at": common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("fine-tuning poll lease lost")
	}
	return nil
}

func ClaimFineTuningSettlement(id string) (bool, error) {
	result := DB.Model(&FineTuningJob{}).Where("id = ? AND billing_settled_at = 0 AND status IN ?", id,
		[]string{FineTuningStatusSucceeded, FineTuningStatusFailed, FineTuningStatusCancelled}).Update("billing_settled_at", -1)
	return result.RowsAffected == 1, result.Error
}

func FinishFineTuningSettlement(id string, succeeded bool) error {
	value := int64(0)
	if succeeded {
		value = common.GetTimestamp()
	}
	return DB.Model(&FineTuningJob{}).Where("id = ? AND billing_settled_at = -1", id).Update("billing_settled_at", value).Error
}

func IsFineTuningTerminal(status string) bool {
	return status == FineTuningStatusSucceeded || status == FineTuningStatusFailed || status == FineTuningStatusCancelled
}
