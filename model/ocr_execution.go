package model

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"time"
)

// OCRExecution is frozen before reservation. Channel/model/group/price remain
// the funding contract; post-reservation cross-channel fallback is unsupported.
type OCRExecution struct {
	ReadProofVersion string   `json:"read_proof_version,omitempty"`
	ResourceOrigins  []string `json:"resource_origins,omitempty"`
	KeySHA256        string   `json:"key_sha256,omitempty"`
	RequestedModel   string   `json:"requested_model,omitempty"`
	JobsURL          string   `json:"jobs_url"`
	Model            string   `json:"model"`
	ProfileID        int      `json:"profile_id"`
	InputPages       int      `json:"input_pages"`
	InputDigest      string   `json:"input_digest"`
	MaxAttempts      int      `json:"max_attempts"`
}
type OCRSubmissionAttempt struct {
	ClaimID        string            `json:"claim_id"`
	MemberID       string            `json:"member_id"`
	HealthMemberID string            `json:"health_member_id"`
	Binding        OCRAccountBinding `json:"binding"`
	State          string            `json:"state"`
	Status         int               `json:"status,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

func (s *OCRBillingStore) ConfigureOCRSubmission(ctx context.Context, id string, execution OCRExecution) error {
	if execution.MaxAttempts < 1 || execution.MaxAttempts > 16 || len(execution.JobsURL) > 2048 || execution.JobsURL == "" || execution.Model == "" || len(execution.Model) > 128 || execution.InputDigest == "" {
		return ErrOCRConflict
	}
	raw, err := common.Marshal(execution)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.Execution != "" {
			if j.Execution != string(raw) {
				return ErrOCRConflict
			}
			return nil
		}
		if j.BillingState != "pending" || j.SubmissionState != "unclaimed" {
			return ErrOCRConflict
		}
		j.Execution = string(raw)
		j.MaxAttempts = execution.MaxAttempts
		return ocrSaveJob(tx, j)
	})
}
func ocrUpdateAttempt(j *OCRJob, state string, status int) error {
	if j.Attempts == "" {
		return nil
	}
	var attempts []OCRSubmissionAttempt
	if err := common.UnmarshalJsonStr(j.Attempts, &attempts); err != nil {
		return err
	}
	if len(attempts) == 0 || attempts[len(attempts)-1].ClaimID != j.ClaimID {
		return ErrOCRConflict
	}
	last := &attempts[len(attempts)-1]
	last.State = state
	last.Status = status
	last.UpdatedAt = time.Now().Unix()
	raw, err := common.Marshal(attempts)
	j.Attempts = string(raw)
	return err
}

// Only these HTTP statuses prove create was refused without execution. Caller
// also carries the actual retry veto/budget/cancellation decision. Never rearm
// transport errors, 5xx, arbitrary text, or a previously accepted job.
func (s *OCRBillingStore) RecordOCRRefusal(ctx context.Context, c OCRClaim, status int, retry bool) error {
	if status != 401 && status != 429 {
		return ErrOCRConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, c.JobID)
		if err != nil {
			return err
		}
		if j.ClaimID != c.ClaimID || j.MemberID != c.MemberID || j.Decision != "" || j.ProviderJobID != "" || j.SubmissionState != "submitting" {
			return ErrOCRConflict
		}
		j.SubmissionState = "rejected"
		j.RetryRefusal = retry && j.SubmitAttempts < j.MaxAttempts
		// A terminal refusal is definitive evidence of no execution. Persist
		// its decision with that evidence so status-only recovery can resume
		// Finish after interruption without replaying the upload or supplier.
		if !j.RetryRefusal {
			j.Decision = "failure"
		}
		if err := ocrUpdateAttempt(j, "refused", status); err != nil {
			return err
		}
		return ocrSaveJob(tx, j)
	})
}

// Provider progress/result URL is private evidence; terminal state never regresses.
func (s *OCRBillingStore) RecordOCRProgress(ctx context.Context, id, claim, state, resultURL string, total, extracted int) error {
	if total < 0 || extracted < 0 || extracted > total || total > 200 || len(resultURL) > 8192 {
		return ErrOCRConflict
	}
	if state != "pending" && state != "running" && state != "done" && state != "failed" {
		return ErrOCRConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.Funding == "platform" && state == "done" && (total < 1 || extracted != total || resultURL == "") {
			return ErrOCRConflict
		}
		if j.ClaimID != claim || j.SubmissionState != "submitted" {
			return ErrOCRConflict
		}
		if j.ProviderState == "done" || j.ProviderState == "failed" {
			if j.ProviderState != state {
				return ErrOCRConflict
			}
			if j.Funding == "platform" && state == "done" && j.ResultResourceID == "" {
				if j.TotalPages != total || j.ExtractedPages != extracted {
					return ErrOCRConflict
				}
				j.ResultURL = resultURL
				return ocrSaveJob(tx, j)
			}
			return nil
		}
		j.ProviderState = state
		j.TotalPages = total
		j.ExtractedPages = extracted
		j.ResultURL = resultURL
		return ocrSaveJob(tx, j)
	})
}

// AbandonOCRSubmission fences a local cancellation or exhausted healthy pool
// before issuing the financial finish. A concurrent accepted/submitting claim
// can never be mistaken for a definitive supplier failure and refunded.
func (s *OCRBillingStore) AbandonOCRSubmission(ctx context.Context, id string) (OCRJob, error) {
	outcome := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.ProviderJobID != "" {
			return ErrOCRConflict
		}
		switch j.SubmissionState {
		case "unclaimed":
			outcome = "cancel"
		case "rejected":
			outcome = "failure"
		default:
			return ErrOCRConflict
		}
		if j.Decision != "" && j.Decision != outcome {
			return ErrOCRConflict
		}
		j.Decision = outcome
		j.RetryRefusal = false
		return ocrSaveJob(tx, j)
	})
	if err != nil {
		return OCRJob{}, err
	}
	return s.Finish(ctx, id, outcome)
}
