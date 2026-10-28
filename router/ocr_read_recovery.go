package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"reflect"
)

// This is called only after a distinct UniHub original-owner read verification.
// It fills missing metadata; no execution, money command, or target selection occurs.
func recoverOwnedOCRRead(ctx context.Context, a *platformbilling.Adapter, row platformbilling.Call) error {
	if controller.OCRRuntime == nil {
		return platformbilling.ErrInvalid
	}
	db := controller.OCRRuntime.DB.WithContext(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&model.OCRJob{}).Where("platform_call_id = ? AND funding = ? AND provider_job_id = ?", row.CallID, "platform", row.ProviderTaskID).UpdateColumn("version", gorm.Expr("version")).Error; e != nil {
			return e
		}
		var job model.OCRJob
		if e := tx.Where("platform_call_id = ? AND funding = ? AND provider_job_id = ?", row.CallID, "platform", row.ProviderTaskID).First(&job).Error; e != nil {
			return e
		}
		var execution model.OCRExecution
		var binding model.OCRAccountBinding
		var attempts []model.OCRSubmissionAttempt
		var attempt platformbilling.Attempt
		if common.UnmarshalJsonStr(job.Execution, &execution) != nil || common.UnmarshalJsonStr(job.AccountBinding, &binding) != nil || common.UnmarshalJsonStr(job.Attempts, &attempts) != nil || len(attempts) != 1 || tx.Where("call_id = ?", row.CallID).First(&attempt).Error != nil {
			return platformbilling.ErrInvalid
		}
		var attemptCount int64
		if e := tx.Model(&platformbilling.Attempt{}).Where("call_id = ?", row.CallID).Count(&attemptCount).Error; e != nil {
			return e
		}
		original := attempts[0]
		if attemptCount != 1 || attempt.ChannelID != job.ChannelID || attempt.Status != "DISPATCHED" || job.SubmissionState != "submitted" || binding.Ref.ChannelID != job.ChannelID || binding.Ref.MemberID == "" || binding.Ref.MemberID != job.MemberID || (binding.Owner != nil && !reflect.DeepEqual(*binding.Owner, binding.Ref)) || original.ClaimID != job.ClaimID || original.MemberID != job.MemberID || original.State != "submitted" || !reflect.DeepEqual(original.Binding, binding) || execution.ProfileID != 0 || execution.Model != job.Model {
			return platformbilling.ErrInvalid
		}
		// Protect all authenticated Call scope against concurrent replacement.
		var current platformbilling.Call
		if e := tx.First(&current, "call_id = ?", row.CallID).Error; e != nil {
			return e
		}
		if current.Digest != row.Digest || current.CredentialID != row.CredentialID || current.CredentialKind != row.CredentialKind || current.ActorID != row.ActorID || current.AccountID != row.AccountID || current.OriginProductID != row.OriginProductID || current.InstanceID != row.InstanceID || current.Model != row.Model || current.TaskID != row.TaskID || current.WireOperation != row.WireOperation || current.ProviderTaskID != row.ProviderTaskID {
			return platformbilling.ErrConflict
		}
		if execution.RequestedModel != "" && execution.KeySHA256 != "" && len(execution.ResourceOrigins) > 0 {
			return nil
		}
		p, ok := a.OCRReadCertificate(row.Model)
		if !ok || p.ChannelID != job.ChannelID || p.UpstreamModel != execution.Model || p.Origin+"/api/v2/ocr/jobs" != execution.JobsURL {
			return platformbilling.ErrInvalid
		}
		var channel model.Channel
		if e := tx.First(&channel, job.ChannelID).Error; e != nil {
			return e
		}
		if channel.Type != p.ChannelType {
			return platformbilling.ErrInvalid
		}
		key, e := model.ResolveTaskAccountKey(ctx, tx, &channel, binding.Ref)
		if e != nil {
			return e
		}
		sum := sha256.Sum256([]byte(key))
		if hex.EncodeToString(sum[:]) != p.ChannelKeySHA256 {
			return platformbilling.ErrInvalid
		}
		if (execution.RequestedModel != "" && execution.RequestedModel != row.Model) || (execution.KeySHA256 != "" && execution.KeySHA256 != p.ChannelKeySHA256) || (len(execution.ResourceOrigins) > 0 && !reflect.DeepEqual(execution.ResourceOrigins, p.ResourceOrigins)) || (execution.ReadProofVersion != "" && execution.ReadProofVersion != "owned-call-exact-certificate-v1") {
			return platformbilling.ErrConflict
		}

		execution.RequestedModel = row.Model
		execution.KeySHA256 = p.ChannelKeySHA256
		execution.ResourceOrigins = p.ResourceOrigins
		execution.ReadProofVersion = "owned-call-exact-certificate-v1"
		raw, e := common.Marshal(execution)
		if e != nil {
			return e
		}
		updated := tx.Model(&model.OCRJob{}).Where("id = ? AND execution = ? AND version = ? AND account_binding = ? AND attempts = ?", job.ID, job.Execution, job.Version, job.AccountBinding, job.Attempts).Updates(map[string]any{"execution": string(raw), "version": job.Version + 1})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return platformbilling.ErrConflict
		}
		return nil
	})
}
