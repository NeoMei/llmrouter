package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNativeQuotaNotEnrolled      = errors.New("native token quota protocol is not enrolled")
	ErrNativeQuotaConflict         = errors.New("native token quota operation conflicts with its durable receipt")
	ErrNativeQuotaUnknown          = errors.New("native token quota operation requires receipt reconciliation")
	ErrNativeFundingReconciliation = errors.New("native funding operation requires reconciliation; automatic replay is prohibited")
)

// Protocol activation is an explicit operator action after every old writer has
// stopped and drained. Schema migration alone never imports a balance.
type NativeTokenQuotaProtocol struct {
	ID              int `gorm:"primaryKey"`
	Version         int
	CutoverEvidence string `gorm:"size:64"`
	ManifestSHA256  string `gorm:"size:64"`
}

type NativeTokenQuotaState struct {
	TokenID           int `gorm:"primaryKey;autoIncrement:false"`
	RemainQuota       int
	UsedQuota         int
	PolicyGeneration  int64
	Revision          int64
	ProjectedRevision int64
	ImportEvidence    string `gorm:"size:64"`
}

// The primary key binds token + caller operation ID. Immutable input is checked
// on every replay, including a declined admission. No secret is journaled.
type NativeTokenQuotaOperation struct {
	ID          string `gorm:"primaryKey;size:64"`
	TokenID     int    `gorm:"index"`
	UserID      int
	OperationID string `gorm:"size:160"`
	SessionID   string `gorm:"size:128"`
	Kind        string `gorm:"size:24"`
	Amount      int
	Accepted    bool
	Outcome     string `gorm:"size:24"`
	Revision    int64
	CreatedAt   int64
}

type NativeTokenQuotaSession struct {
	ID            string `gorm:"primaryKey;size:128"`
	TokenID       int    `gorm:"index"`
	UserID        int
	ReservedQuota int
	FinalQuota    int
	State         string `gorm:"size:24"`
	// Funding is the original engine. An intent is durable BEFORE calling it;
	// process loss or an ambiguous result leaves UNKNOWN, never a replay.
	FundingStage  string `gorm:"size:160"`
	FundingAmount int
	FundingState  string `gorm:"size:24"`
}

// Every funding stage keeps its own durable intent. A later stage must not
// erase the evidence that prevents a reconstructed session replaying an earlier
// wallet debit/refund after its in-memory flags were lost.
type NativeTokenFundingOperation struct {
	ID        string `gorm:"primaryKey;size:64"`
	SessionID string `gorm:"size:128;index"`
	Stage     string `gorm:"size:160"`
	Amount    int
	State     string `gorm:"size:24"`
}

type NativeTokenQuotaImport struct {
	TokenID          int    `json:"token_id"`
	UserID           int    `json:"user_id"`
	KeySHA256        string `json:"key_sha256"`
	RemainQuota      int    `json:"remain_quota"`
	UsedQuota        int    `json:"used_quota"`
	PolicyGeneration int64  `json:"policy_generation"`
	UnlimitedQuota   bool   `json:"unlimited_quota"`
	Status           int    `json:"status"`
	ExpiredTime      int64  `json:"expired_time"`
}

type NativeTokenQuotaInventory struct {
	TokenID   int    `json:"token_id"`
	UserID    int    `json:"user_id"`
	KeySHA256 string `json:"key_sha256"`
	Deleted   bool   `json:"deleted"`
}

type NativeTokenQuotaManifest struct {
	Version         int                         `json:"version"`
	CutoverEvidence string                      `json:"cutover_evidence"`
	Tokens          []NativeTokenQuotaImport    `json:"tokens"`
	Inventory       []NativeTokenQuotaInventory `json:"inventory,omitempty"`
}

func RegisterNativeTokenQuotaModels(db *gorm.DB) error {
	return db.AutoMigrate(&NativeTokenQuotaProtocol{}, &NativeTokenQuotaState{}, &NativeTokenQuotaOperation{}, &NativeTokenQuotaSession{}, &NativeTokenFundingOperation{})
}

// ImportNativeTokenQuota requires separately approved frozen evidence. Callers
// must stop/drain all legacy writers first; old binaries cannot honor this gate.
// It never changes a key's realm, balance, secret, owner or restrictions.
func ImportNativeTokenQuota(ctx context.Context, db *gorm.DB, raw []byte, approvedSHA256 string) error {
	sum := sha256.Sum256(raw)
	if db == nil || ctx == nil || len(raw) == 0 || len(raw) > 4<<20 || hex.EncodeToString(sum[:]) != approvedSHA256 {
		return ErrNativeQuotaConflict
	}
	var manifest NativeTokenQuotaManifest
	if common.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || len(manifest.CutoverEvidence) != 64 {
		return ErrNativeQuotaConflict
	}
	if _, err := hex.DecodeString(manifest.CutoverEvidence); err != nil {
		return ErrNativeQuotaConflict
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var protocol NativeTokenQuotaProtocol
		err := lockForUpdate(tx).First(&protocol, 1).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			protocol = NativeTokenQuotaProtocol{ID: 1, Version: 1, CutoverEvidence: manifest.CutoverEvidence, ManifestSHA256: approvedSHA256}
			if err = tx.Create(&protocol).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if protocol.Version != 1 || protocol.CutoverEvidence != manifest.CutoverEvidence {
			return ErrNativeQuotaConflict
		}
		inventory := make(map[int]bool, len(manifest.Inventory))
		for _, expected := range manifest.Inventory {
			if expected.TokenID <= 0 || inventory[expected.TokenID] {
				return ErrNativeQuotaConflict
			}
			inventory[expected.TokenID] = true
			var token Token
			if err := tx.Unscoped().First(&token, expected.TokenID).Error; err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(token.Key))
			if token.UserId != expected.UserID || token.DeletedAt.Valid != expected.Deleted || hex.EncodeToString(digest[:]) != expected.KeySHA256 {
				return ErrNativeQuotaConflict
			}
		}
		seen := make(map[int]bool, len(manifest.Tokens))
		for _, expected := range manifest.Tokens {
			if expected.TokenID <= 0 || seen[expected.TokenID] {
				return ErrNativeQuotaConflict
			}
			seen[expected.TokenID] = true
			token, err := lockNativeQuotaToken(tx, expected.TokenID)
			if err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(token.Key))
			if token.DeletedAt.Valid || token.AuthRealm != NativeFundingLocalOnly || token.FundingAccountID != "" || token.UserId != expected.UserID || hex.EncodeToString(digest[:]) != expected.KeySHA256 || token.RemainQuota != expected.RemainQuota || token.UsedQuota != expected.UsedQuota || token.QuotaPolicyGeneration != expected.PolicyGeneration || token.UnlimitedQuota != expected.UnlimitedQuota || token.Status != expected.Status || token.ExpiredTime != expected.ExpiredTime {
				return ErrNativeQuotaConflict
			}
			var state NativeTokenQuotaState
			err = tx.First(&state, "token_id = ?", token.Id).Error
			if err == nil {
				if state.ImportEvidence != approvedSHA256 {
					return ErrNativeQuotaConflict
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			state = NativeTokenQuotaState{TokenID: token.Id, RemainQuota: token.RemainQuota, UsedQuota: token.UsedQuota, PolicyGeneration: token.QuotaPolicyGeneration, Revision: 1, ProjectedRevision: 1, ImportEvidence: approvedSHA256}
			if err = tx.Create(&state).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func lockNativeQuotaToken(tx *gorm.DB, id int, includeDeleted ...bool) (Token, error) {
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&Token{}).Where("id = ?", id).UpdateColumn("quota_policy_generation", gorm.Expr("quota_policy_generation")).Error; err != nil {
			return Token{}, err
		}
	}
	var token Token
	query := tx
	if len(includeDeleted) > 0 && includeDeleted[0] {
		query = tx.Unscoped()
	}
	err := lockForUpdate(query).First(&token, id).Error
	return token, err
}

func nativeQuotaState(tx *gorm.DB, token Token) (NativeTokenQuotaState, error) {
	var state NativeTokenQuotaState
	err := tx.First(&state, "token_id = ?", token.Id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return state, ErrNativeQuotaNotEnrolled
	}
	if err != nil {
		return state, err
	}
	if state.ImportEvidence == "" || state.PolicyGeneration != token.QuotaPolicyGeneration {
		return state, ErrNativeQuotaConflict
	}
	return state, nil
}

func nativeQuotaOperationKey(tokenID int, operationID string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", tokenID, operationID)))
	return hex.EncodeToString(digest[:])
}

func NativeTokenQuotaOperationReceipt(id int, operationID string) (NativeTokenQuotaOperation, error) {
	var receipt NativeTokenQuotaOperation
	err := DB.First(&receipt, "id = ?", nativeQuotaOperationKey(id, operationID)).Error
	return receipt, err
}

func nativeQuotaOperation(id int, key, operationID, sessionID, kind string, amount int) (bool, error) {
	if strings.TrimSpace(operationID) == "" || len(operationID) > 160 || len(sessionID) > 128 || amount < 0 {
		return false, ErrNativeQuotaConflict
	}
	var identityToken Token
	if err := DB.Unscoped().First(&identityToken, id).Error; err != nil {
		return false, err
	}
	if identityToken.AuthRealm != NativeFundingLocalOnly || identityToken.Key != key || identityToken.FundingAccountID != "" {
		return false, ErrNativeCredentialUnverified
	}
	request := NativeTokenQuotaOperation{ID: nativeQuotaOperationKey(id, operationID), TokenID: id, UserID: identityToken.UserId, OperationID: operationID, SessionID: sessionID, Kind: kind, Amount: amount, CreatedAt: common.GetTimestamp(), Outcome: "prepared"}
	prepared := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&request)
	if prepared.Error != nil {
		return false, fmt.Errorf("%w: prepare receipt: %v", ErrNativeQuotaUnknown, prepared.Error)
	}
	if prepared.RowsAffected == 0 {
		prior, err := NativeTokenQuotaOperationReceipt(id, operationID)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrNativeQuotaUnknown, err)
		}
		if prior.UserID != identityToken.UserId || prior.OperationID != operationID || prior.Kind != kind || prior.Amount != amount || prior.SessionID != sessionID {
			return false, ErrNativeQuotaConflict
		}
		if prior.Outcome == "accepted" || prior.Outcome == "declined" {
			return prior.Accepted, nil
		}
		if prior.Outcome != "retryable" {
			return false, ErrNativeQuotaUnknown
		}
		claim := DB.Model(&NativeTokenQuotaOperation{}).Where("id = ? AND outcome = ?", request.ID, "retryable").Update("outcome", "prepared")
		if claim.Error != nil || claim.RowsAffected != 1 {
			return false, ErrNativeQuotaUnknown
		}
	}
	accepted := false
	// Never return success from the transaction callback: only COMMIT or a
	// durable same-operation readback proves the receipt was accepted.
	err := nativeQuotaTransaction(DB, func(tx *gorm.DB) error {
		token, err := lockNativeQuotaToken(tx, id, kind != "reserve")
		if err != nil {
			return err
		}
		if token.AuthRealm != NativeFundingLocalOnly || token.Key != key || token.FundingAccountID != "" || token.UserId != identityToken.UserId {
			return ErrNativeCredentialUnverified
		}
		state, err := nativeQuotaState(tx, token)
		if err != nil {
			return err
		}
		delta := amount
		var session NativeTokenQuotaSession
		if sessionID != "" {
			err = tx.First(&session, "id = ?", sessionID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) && kind == "reserve" {
				session = NativeTokenQuotaSession{ID: sessionID, TokenID: id, UserID: token.UserId, State: "active"}
			} else if err != nil {
				return err
			}
			if session.TokenID != id || session.UserID != token.UserId || session.State != "active" {
				return ErrNativeQuotaConflict
			}
			if session.FundingState == "unknown" {
				return ErrNativeFundingReconciliation
			}
			if strings.HasPrefix(session.FundingStage, "settle:") && session.FundingStage != fmt.Sprintf("settle:%d", amount) {
				return ErrNativeQuotaConflict
			}
			if session.FundingStage == "refund" && kind != "refund" {
				return ErrNativeQuotaConflict
			}
			switch kind {
			case "reserve":
				if amount < session.ReservedQuota {
					return ErrNativeQuotaConflict
				}
				delta = amount - session.ReservedQuota
			case "settle":
				delta = amount - session.ReservedQuota
			case "refund":
				delta = -session.ReservedQuota
			default:
				return ErrNativeQuotaConflict
			}
		} else if kind == "credit" {
			delta = -amount
		} else if kind != "reserve" && kind != "debit" {
			return ErrNativeQuotaConflict
		}
		// Current SQL policy participates in the same lock/commit as admission.
		if kind == "reserve" && (token.DeletedAt.Valid || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp())) {
			return ErrNativeCredentialUnverified
		}
		accepted = kind != "reserve" || token.UnlimitedQuota || state.RemainQuota >= delta
		if accepted {
			remain, balanceErr := nativeQuotaCheckedAdd(state.RemainQuota, -delta)
			if balanceErr != nil {
				return balanceErr
			}
			used, balanceErr := nativeQuotaCheckedAdd(state.UsedQuota, delta)
			if balanceErr != nil || state.Revision == math.MaxInt64 {
				return ErrNativeQuotaConflict
			}
			state.RemainQuota, state.UsedQuota, state.Revision = remain, used, state.Revision+1
			if err = tx.Save(&state).Error; err != nil {
				return err
			}
			if sessionID != "" {
				switch kind {
				case "reserve":
					session.ReservedQuota = amount
				case "settle":
					session.State, session.FinalQuota = "settled", amount
				case "refund":
					session.State, session.FinalQuota = "refunded", 0
				}
				if err = tx.Save(&session).Error; err != nil {
					return err
				}
			}
			if !common.BatchUpdateEnabled {
				if err = projectNativeQuotaTx(tx, token, &state); err != nil {
					return err
				}
			}
		}
		request.Accepted, request.Revision = accepted, state.Revision
		request.Outcome = "declined"
		if accepted {
			request.Outcome = "accepted"
		}
		result := tx.Model(&NativeTokenQuotaOperation{}).Where("id = ? AND outcome = ?", request.ID, "prepared").Updates(map[string]any{"accepted": accepted, "revision": state.Revision, "outcome": request.Outcome})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNativeQuotaConflict
		}
		return nil
	})
	if err != nil {
		prior, readErr := NativeTokenQuotaOperationReceipt(id, operationID)
		if readErr == nil {
			if prior.UserID != identityToken.UserId || prior.Kind != kind || prior.Amount != amount || prior.SessionID != sessionID {
				return false, ErrNativeQuotaConflict
			}
			if prior.Outcome == "accepted" || prior.Outcome == "declined" {
				accepted, err = prior.Accepted, nil
			} else if errors.Is(err, ErrNativeQuotaUnknown) {
				return false, err
			} else {
				_ = DB.Model(&NativeTokenQuotaOperation{}).Where("id = ? AND outcome = ?", request.ID, "prepared").Update("outcome", "retryable").Error
				return false, err
			}
		} else if !errors.Is(readErr, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("%w: %v; receipt read: %v", ErrNativeQuotaUnknown, err, readErr)
		} else {
			return false, err
		}
	}
	// A projection failure cannot undo a committed receipt or create capacity.
	if common.RedisEnabled {
		if _, cacheErr := GetTokenByKey(key, true); cacheErr != nil {
			common.SysLog("native token quota cache refresh pending: " + cacheErr.Error())
		}
	}
	return accepted, err
}

func ReserveNativeTokenQuota(id int, key, sessionID string, targetQuota int) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, ErrNativeQuotaConflict
	}
	return nativeQuotaOperation(id, key, fmt.Sprintf("%s:reserve:%d", sessionID, targetQuota), sessionID, "reserve", targetQuota)
}
func SettleNativeTokenQuota(id int, key, sessionID string, actualQuota int) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrNativeQuotaConflict
	}
	_, err := nativeQuotaOperation(id, key, fmt.Sprintf("%s:settle:%d", sessionID, actualQuota), sessionID, "settle", actualQuota)
	return err
}
func RefundNativeTokenQuota(id int, key, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrNativeQuotaConflict
	}
	_, err := nativeQuotaOperation(id, key, sessionID+":refund", sessionID, "refund", 0)
	return err
}

func projectNativeQuotaTx(tx *gorm.DB, token Token, state *NativeTokenQuotaState) error {
	result := tx.Unscoped().Model(&Token{}).Where("id = ? AND auth_realm = ? AND quota_policy_generation = ?", token.Id, NativeFundingLocalOnly, state.PolicyGeneration).Updates(map[string]any{"remain_quota": state.RemainQuota, "used_quota": state.UsedQuota, "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNativeQuotaConflict
	}
	state.ProjectedRevision = state.Revision
	return tx.Model(state).Update("projected_revision", state.Revision).Error
}

// A durable dirty scan is intentional: an instance can exit with no surviving
// RAM wakeup, and another batch worker must still finish its accepted work.
func FlushNativeTokenQuotaProjections() error {
	var dirty []NativeTokenQuotaState
	if err := DB.Where("revision <> projected_revision").Find(&dirty).Error; err != nil {
		return err
	}
	for _, pending := range dirty {
		if err := DB.Transaction(func(tx *gorm.DB) error {
			token, err := lockNativeQuotaToken(tx, pending.TokenID, true)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			state, err := nativeQuotaState(tx, token)
			if err != nil {
				return err
			}
			return projectNativeQuotaTx(tx, token, &state)
		}); err != nil {
			return err
		}
	}
	return nil
}

// Funding intent and result are deliberately separate from token completion.
// UNKNOWN is durable before the external legacy engine call and never replays
// automatically after a crash, even when its token receipt is recoverable.
func BeginNativeTokenFundingStage(id int, sessionID, stage string, amount int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockNativeQuotaToken(tx, id, true); err != nil {
			return err
		}
		var session NativeTokenQuotaSession
		if err := tx.First(&session, "id = ? AND token_id = ?", sessionID, id).Error; err != nil {
			return err
		}
		if session.State != "active" || session.FundingState == "unknown" {
			return ErrNativeFundingReconciliation
		}
		digest := sha256.Sum256([]byte(sessionID + "\x00" + stage))
		operation := NativeTokenFundingOperation{ID: hex.EncodeToString(digest[:]), SessionID: sessionID, Stage: stage, Amount: amount, State: "unknown"}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&operation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNativeFundingReconciliation
		}
		return tx.Model(&session).Updates(map[string]any{"funding_stage": stage, "funding_amount": amount, "funding_state": "unknown"}).Error
	})
}
func FinishNativeTokenFundingStage(id int, sessionID, stage, outcome string) error {
	if outcome != "confirmed" && outcome != "declined" {
		return ErrNativeQuotaConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockNativeQuotaToken(tx, id, true); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(sessionID + "\x00" + stage))
		fundingResult := tx.Model(&NativeTokenFundingOperation{}).Where("id = ? AND state = ?", hex.EncodeToString(digest[:]), "unknown").Update("state", outcome)
		if fundingResult.Error != nil {
			return fundingResult.Error
		}
		if fundingResult.RowsAffected != 1 {
			return ErrNativeFundingReconciliation
		}
		result := tx.Model(&NativeTokenQuotaSession{}).Where("id = ? AND token_id = ? AND funding_stage = ? AND funding_state = ?", sessionID, id, stage, "unknown").Update("funding_state", outcome)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNativeFundingReconciliation
		}
		return nil
	})
}

// NativeTokenQuotaBalance reads the authoritative admitted balance, including
// refunds and holds whose token-table batch projection has not run yet.
func NativeTokenQuotaBalance(ctx context.Context, tokenID int, policyGeneration int64) (int, int, error) {
	var state NativeTokenQuotaState
	err := DB.WithContext(ctx).First(&state, "token_id = ?", tokenID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, ErrNativeQuotaNotEnrolled
	}
	if err != nil {
		return 0, 0, err
	}
	if state.PolicyGeneration != policyGeneration || state.ImportEvidence == "" {
		return 0, 0, ErrNativeQuotaConflict
	}
	return state.RemainQuota, state.UsedQuota, nil
}

// Pin the connection through commit-error cleanup. SQLite's deferred FK COMMIT
// failure leaves its connection inside the uncommitted transaction after
// database/sql has closed Tx, so readback must first rollback that connection.
func nativeQuotaTransaction(db *gorm.DB, apply func(*gorm.DB) error) error {
	return db.Connection(func(conn *gorm.DB) error {
		var callbackErr error
		err := conn.Transaction(func(tx *gorm.DB) error { callbackErr = apply(tx); return callbackErr })
		if err != nil && conn.Dialector.Name() == "sqlite" {
			_ = conn.Exec("ROLLBACK").Error
		}
		if err != nil && callbackErr == nil {
			return fmt.Errorf("%w: transaction result: %v", ErrNativeQuotaUnknown, err)
		}
		return err
	})
}

func nativeQuotaCheckedAdd(balance, delta int) (int, error) {
	if (delta > 0 && balance > math.MaxInt-delta) || (delta < 0 && balance < math.MinInt-delta) {
		return 0, ErrNativeQuotaConflict
	}
	return balance + delta, nil
}

// The existing OCR operation is the idempotency receipt for this adapter. Both
// its applied flag and native quota state commit in the caller's transaction.
func applyNativeOCRTokenQuotaTx(tx *gorm.DB, job *OCRJob, operation *OCROperation, token Token, delta int) error {
	if operation.Amount < 0 || token.FundingAccountID != "" || token.UserId != job.UserID {
		return ErrOCRConflict
	}
	if operation.Phase == "reserve" && (token.DeletedAt.Valid || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp())) {
		return ErrOCRConflict
	}
	state, err := nativeQuotaState(tx, token)
	if err != nil {
		return err
	}
	if (operation.Phase == "reserve" || operation.Phase == "native_debit") && !token.UnlimitedQuota && state.RemainQuota < operation.Amount {
		return ErrOCRInsufficient
	}
	remain, err := nativeQuotaCheckedAdd(state.RemainQuota, delta)
	if err != nil {
		return err
	}
	used, err := nativeQuotaCheckedAdd(state.UsedQuota, -delta)
	if err != nil || state.Revision == math.MaxInt64 {
		return ErrNativeQuotaConflict
	}
	state.RemainQuota, state.UsedQuota, state.Revision = remain, used, state.Revision+1
	if err = tx.Save(&state).Error; err != nil {
		return err
	}
	if !common.BatchUpdateEnabled {
		if err = projectNativeQuotaTx(tx, token, &state); err != nil {
			return err
		}
	}
	return ocrApplied(tx, operation)
}
