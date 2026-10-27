package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
)

var ErrProductFundingPending = errors.New("product funding pending reconciliation")
var ErrProductFundingConflict = errors.New("product funding contract conflict")
var ErrProductFundingMigration = errors.New("existing product balance requires reviewed funding cutover")

// ProductFundingAccount is a funding cutover receipt, not a spendable balance.
// SnapshotQuota is retained for audit; only UniHub's ledger can spend it.
type ProductFundingAccount struct {
	ScopeDigest          string `gorm:"size:64"`
	UserID               int    `gorm:"primaryKey"`
	AccountID            string `gorm:"size:128"`
	Asset                string `gorm:"size:32"`
	UnitVersion          string `gorm:"size:32"`
	State                string `gorm:"size:24;index"`
	SnapshotQuota        int
	SnapshotDigest       string `gorm:"size:64"`
	CutoverEvidence      string `gorm:"type:text"`
	BatchID              string `gorm:"size:128"`
	CreatedAt, UpdatedAt int64
}

// ProductRuntimeCall is the durable financial intent for an original product
// call. It never stores a user credential, API key secret, or request body.
type ProductRuntimeCall struct {
	ParentCallID                             string `gorm:"size:128;index"`
	FinancialCommitment                      bool
	ScopeDigest                              string `gorm:"size:64"`
	ExternalCounters                         bool
	ID                                       string `gorm:"primaryKey;size:128"`
	UserID, TokenID                          int
	AccountID                                string `gorm:"size:128"`
	ContractDigest                           string `gorm:"size:64"`
	ReserveJSON                              string `gorm:"type:text"`
	PricingDigest                            string `gorm:"size:64"`
	State                                    string `gorm:"size:24;index"`
	ReservedQuota, TokenReserved, FinalQuota int
	FinalUsage                               string `gorm:"type:text"`
	FinalDigest                              string `gorm:"size:64"`
	CreatedAt, UpdatedAt                     int64
	DispatchedAt, DispatchLeaseUntil         int64
}

type ProductFundingAdmission struct {
	ParentCallID                    string
	FinancialCommitment             bool
	ExternalCounters                bool
	CallID                          string
	UserID, TokenID                 int
	Model, Operation, PriceSnapshot string
	Quota                           int
}

type ProductFundingStore struct {
	db     *gorm.DB
	client *userkit.RuntimeClient
}

type ProductFundingTopUp struct {
	AcceptedCall         bool
	ID                   string `gorm:"primaryKey;size:128"`
	CallID               string `gorm:"index;size:128"`
	TargetQuota, Delta   int
	State                string `gorm:"size:24;index"`
	CreatedAt, UpdatedAt int64
}

func NewProductFundingStore(db *gorm.DB, client *userkit.RuntimeClient) *ProductFundingStore {
	return &ProductFundingStore{db: db, client: client}
}

func productDigest(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func productFundingStore() (*ProductFundingStore, error) {
	client, err := platformaccount.RuntimeClient()
	if err != nil || client == nil || DB == nil {
		return nil, ErrProductFundingPending
	}
	return NewProductFundingStore(DB, client), nil
}

func productQuota(raw string) (int, error) {
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 || n > int64(common.MaxQuota) || strconv.FormatInt(n, 10) != raw {
		return 0, ErrProductFundingConflict
	}
	return int(n), nil
}

func (s *ProductFundingStore) Account(ctx context.Context, id int) (ProductFundingAccount, error) {
	var a ProductFundingAccount
	err := s.db.WithContext(ctx).First(&a, "user_id = ?", id).Error
	if err == nil && (a.ScopeDigest == "" || a.ScopeDigest != s.ScopeDigest()) {
		return a, ErrProductFundingConflict
	}
	return a, err
}

// Import requires drained legacy admissions. It freezes local spending before
// the remote import, and retains the same immutable receipt after lost replies.
func (s *ProductFundingStore) Import(ctx context.Context, id int, payer, batch string, reviewedEvidence ...string) error {
	evidence := ""
	if len(reviewedEvidence) > 1 {
		return ErrProductFundingConflict
	}
	if len(reviewedEvidence) == 1 {
		evidence = reviewedEvidence[0]
		if len(evidence) > 65536 {
			return ErrProductFundingConflict
		}
	}
	batchUpdateRunMu.Lock()
	defer batchUpdateRunMu.Unlock()
	if common.BatchUpdateEnabled {
		return ErrProductFundingMigration
	}
	for kind := 0; kind < BatchUpdateTypeCount; kind++ {
		batchUpdateLocks[kind].Lock()
		pending := len(batchUpdateStores[kind]) != 0
		batchUpdateLocks[kind].Unlock()
		if pending {
			return ErrProductFundingMigration
		}
	}
	if id <= 0 || payer == "" || batch == "" || len(batch) > 128 {
		return ErrProductFundingConflict
	}
	var a ProductFundingAccount
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, id).Error; err != nil {
			return err
		}
		err := lockForUpdate(tx).First(&a, "user_id = ?", id).Error
		if err == nil {
			if a.ScopeDigest == "" || a.ScopeDigest != s.ScopeDigest() || a.AccountID != payer || a.BatchID != batch || a.CutoverEvidence != evidence {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if user.Quota < 0 {
			return ErrProductFundingConflict
		}
		if err := productLegacyDrained(tx, id); err != nil {
			return err
		}
		a = ProductFundingAccount{ScopeDigest: s.ScopeDigest(), UserID: id, AccountID: payer, Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "migrating", SnapshotQuota: user.Quota, BatchID: batch, CutoverEvidence: evidence, CreatedAt: time.Now().Unix()}
		a.SnapshotDigest = productDigest(fmt.Sprintf("lr-quota-v1:%d:%d:%s:%s", id, user.Quota, payer, batch) + ":" + evidence)
		return tx.Create(&a).Error
	})
	if err != nil || a.State == "central" {
		return err
	}
	if a.State != "migrating" {
		return ErrProductFundingConflict
	}
	result, err := s.client.ImportBalance(ctx, userkit.RuntimeImportRequest{BatchID: batch, LegacyUserID: strconv.Itoa(id), LocalUserID: strconv.Itoa(id), AccountID: payer, SnapshotDigest: a.SnapshotDigest, Amount: strconv.Itoa(a.SnapshotQuota), Asset: a.Asset, UnitVersion: a.UnitVersion, SourceUnit: userkit.RuntimeUnitVersion, Conversion: "identity", SourceEvidence: json.RawMessage(a.CutoverEvidence)})
	if err != nil {
		return err
	}
	if result.AccountID != payer || result.Amount != strconv.Itoa(a.SnapshotQuota) || result.Status != "APPLIED" {
		return ErrProductFundingPending
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current ProductFundingAccount
		if err := lockForUpdate(tx).First(&current, "user_id = ?", id).Error; err != nil {
			return err
		}
		if current.SnapshotDigest != a.SnapshotDigest {
			return ErrProductFundingConflict
		}
		if current.State == "central" {
			return nil
		}
		r := tx.Model(&User{}).Where("id = ? AND quota = ?", id, current.SnapshotQuota).Update("quota", 0)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrProductFundingConflict
		}
		return tx.Model(&current).Updates(map[string]any{"state": "central", "updated_at": time.Now().Unix()}).Error
	})
	if err == nil {
		_ = invalidateUserCache(id)
		_ = InvalidateUserTokensCache(id)
	}
	return err
}

func (s *ProductFundingStore) ActivateZero(ctx context.Context, id int, payer string) error {
	if payer == "" {
		return ErrProductFundingConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var u User
		if err := lockForUpdate(tx).First(&u, id).Error; err != nil {
			return err
		}
		var a ProductFundingAccount
		err := tx.First(&a, "user_id = ?", id).Error
		if err == nil {
			if a.ScopeDigest == "" || a.ScopeDigest != s.ScopeDigest() || a.AccountID != payer || a.State != "central" {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if u.Quota != 0 {
			return ErrProductFundingMigration
		}
		a = ProductFundingAccount{ScopeDigest: s.ScopeDigest(), UserID: id, AccountID: payer, Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "central", CreatedAt: time.Now().Unix()}
		return tx.Create(&a).Error
	})
}

func (s *ProductFundingStore) Balance(ctx context.Context, id int) (int, error) {
	a, err := s.Account(ctx, id)
	if err != nil {
		return 0, err
	}
	if a.State != "central" {
		return 0, ErrProductFundingPending
	}
	b, err := s.client.Balance(ctx, strconv.Itoa(id), a.AccountID)
	if err != nil {
		return 0, err
	}
	if b.AccountID != a.AccountID || b.Asset != a.Asset || b.UnitVersion != a.UnitVersion {
		return 0, ErrProductFundingConflict
	}
	return productQuota(b.Available)
}

func (s *ProductFundingStore) Reserve(ctx context.Context, in ProductFundingAdmission) (ProductRuntimeCall, error) {
	if in.CallID == "" || len(in.CallID) > 128 || strings.ContainsAny(in.CallID, "/\\?#\r\n") || in.UserID <= 0 || in.TokenID < 0 || in.Model == "" || in.Operation == "" || in.PriceSnapshot == "" || in.Quota < 0 || in.Quota > common.MaxQuota {
		return ProductRuntimeCall{}, ErrProductFundingConflict
	}
	a, err := s.Account(ctx, in.UserID)
	if err != nil {
		return ProductRuntimeCall{}, err
	}
	if a.State != "central" {
		return ProductRuntimeCall{}, ErrProductFundingPending
	}
	var call ProductRuntimeCall
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An original settled call is the sole proof of an accepted financial
		// obligation. A boolean commitment alone cannot bypass current access.
		acceptedParent := false
		price := productDigest(in.PriceSnapshot)
		userQuery := lockForUpdate(tx)
		if in.ParentCallID != "" {
			userQuery = userQuery.Unscoped()
		}
		var user User
		if err := userQuery.First(&user, in.UserID).Error; err != nil {
			return err
		}
		if in.ParentCallID != "" {
			var parent ProductRuntimeCall
			if err := lockForUpdate(tx).First(&parent, "id = ?", in.ParentCallID).Error; err != nil {
				return err
			}
			if !in.FinancialCommitment || !in.ExternalCounters || in.ParentCallID == in.CallID || parent.ParentCallID != "" || parent.State != "settled" || parent.ScopeDigest != s.ScopeDigest() || parent.UserID != in.UserID || parent.TokenID != in.TokenID || parent.AccountID != a.AccountID || in.PriceSnapshot != parent.ReserveJSON {
				return ErrProductFundingConflict
			}
			var original userkit.RuntimeReserveRequest
			if err := common.UnmarshalJsonStr(parent.ReserveJSON, &original); err != nil {
				return err
			}
			if original.CallID != parent.ID || original.LocalUserID != strconv.Itoa(in.UserID) || original.LocalKeyID != strconv.Itoa(in.TokenID) || original.AccountID != a.AccountID || original.Model != in.Model || original.Operation != in.Operation || original.Asset != a.Asset || original.UnitVersion != a.UnitVersion || original.PricingDigest != parent.PricingDigest {
				return ErrProductFundingConflict
			}
			acceptedParent, price = true, parent.PricingDigest
		}
		if !acceptedParent && user.Status != common.UserStatusEnabled {
			return ErrProductFundingConflict
		}
		contract := productDigest(fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s:%t:%t:%s", s.ScopeDigest(), in.UserID, in.TokenID, a.AccountID, in.Model, in.Operation, price, in.ExternalCounters, in.FinancialCommitment, in.ParentCallID))
		err := lockForUpdate(tx).First(&call, "id = ?", in.CallID).Error
		if err == nil {
			if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() || call.ContractDigest != contract {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if in.TokenID > 0 {
			var token Token
			tokenQuery := lockForUpdate(tx)
			if acceptedParent {
				tokenQuery = tokenQuery.Unscoped()
			}
			if err := tokenQuery.First(&token, in.TokenID).Error; err != nil {
				return err
			}
			if token.UserId != in.UserID || (!acceptedParent && (token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()))) || (token.FundingAccountID != "" && token.FundingAccountID != a.AccountID) {
				return ErrProductFundingConflict
			}
			if token.FundingAccountID == "" {
				if err := tokenQuery.Model(&token).Update("funding_account_id", a.AccountID).Error; err != nil {
					return err
				}
			}
			if err := productTokenApplyDelta(tx, in.TokenID, in.Quota, acceptedParent); err != nil {
				return err
			}
		}
		req := userkit.RuntimeReserveRequest{ParentCallID: in.ParentCallID, CallID: in.CallID, LocalUserID: strconv.Itoa(in.UserID), LocalKeyID: strconv.Itoa(in.TokenID), AccountID: a.AccountID, RequestDigest: contract, Model: in.Model, Operation: in.Operation, Asset: a.Asset, UnitVersion: a.UnitVersion, PriceVersion: userkit.RuntimeUnitVersion, PricingDigest: price, Amount: strconv.Itoa(in.Quota)}
		raw, err := common.Marshal(req)
		if err != nil {
			return err
		}
		call = ProductRuntimeCall{ParentCallID: in.ParentCallID, FinancialCommitment: in.FinancialCommitment, ScopeDigest: s.ScopeDigest(), ID: in.CallID, UserID: in.UserID, TokenID: in.TokenID, AccountID: a.AccountID, ContractDigest: contract, ReserveJSON: string(raw), PricingDigest: price, ExternalCounters: in.ExternalCounters, State: "reserve_pending", ReservedQuota: in.Quota, TokenReserved: in.Quota, CreatedAt: time.Now().Unix(), DispatchLeaseUntil: time.Now().Unix() + ProductFundingDispatchLeaseSeconds}
		return tx.Create(&call).Error
	})
	if err != nil {
		return call, err
	}
	if call.State == "reserve_pending" {
		if err = s.resolveProductReserve(ctx, call.ID); err != nil {
			return call, err
		}
		if err = s.db.WithContext(ctx).First(&call, "id = ?", call.ID).Error; err != nil {
			return call, err
		}
	}
	if call.State != "reserved" {
		return call, ErrProductFundingConflict
	}
	if in.Quota > call.ReservedQuota {
		if err = s.TopUp(ctx, call.ID, in.Quota); err != nil {
			return call, err
		}
		call.ReservedQuota = in.Quota
	}
	return call, nil
}

func productTokenDelta(tx *gorm.DB, id, charge int) error {
	return productTokenApplyDelta(tx, id, charge, false)
}

func productTokenApplyDelta(tx *gorm.DB, id, charge int, postpaid bool) error {
	if id == 0 || charge == 0 {
		return nil
	}
	var token Token
	if err := lockForUpdate(tx).Unscoped().First(&token, id).Error; err != nil {
		return err
	}
	remain, used := int64(token.RemainQuota)-int64(charge), int64(token.UsedQuota)+int64(charge)
	if (charge > 0 && !postpaid && !token.UnlimitedQuota && token.RemainQuota < charge) || remain < int64(common.MinQuota) || remain > int64(common.MaxQuota) || used < 0 || used > int64(common.MaxQuota) {
		return ErrOCRInsufficient
	}
	return tx.Unscoped().Model(&token).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": time.Now().Unix()}).Error
}

func (s *ProductFundingStore) validateRemote(call ProductRuntimeCall, remote userkit.RuntimeCall) error {
	if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() || remote.CallID != call.ID || remote.ParentCallID != call.ParentCallID || remote.LocalUserID != strconv.Itoa(call.UserID) || remote.LocalKeyID != strconv.Itoa(call.TokenID) || remote.AccountID != call.AccountID || remote.RequestDigest != call.ContractDigest || remote.PricingDigest != call.PricingDigest || remote.Asset != userkit.RuntimeAsset || remote.UnitVersion != userkit.RuntimeUnitVersion {
		return ErrProductFundingConflict
	}
	return nil
}

func (s *ProductFundingStore) TopUp(ctx context.Context, id string, target int) error {
	if target < 0 || target > common.MaxQuota {
		return ErrProductFundingConflict
	}
	var intent ProductFundingTopUp
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if err := lockForUpdate(tx).First(&call, "id = ?", id).Error; err != nil {
			return err
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if call.State != "reserved" && call.State != "settle_pending" {
			return ErrProductFundingConflict
		}
		var pending ProductFundingTopUp
		err := lockForUpdate(tx).Where("call_id = ? AND state = ?", id, "pending").First(&pending).Error
		if err == nil {
			intent = pending
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if target <= call.ReservedQuota {
			return nil
		}
		delta := target - call.TokenReserved
		if delta <= 0 {
			return ErrProductFundingPending
		}
		if err = productTokenApplyDelta(tx, call.TokenID, delta, call.State == "settle_pending" && (call.DispatchedAt > 0 || call.FinancialCommitment)); err != nil {
			return err
		}
		intent = ProductFundingTopUp{AcceptedCall: call.State == "settle_pending" && (call.DispatchedAt > 0 || call.FinancialCommitment), ID: productDigest(fmt.Sprintf("topup:%s:%d:%s", id, target, common.GetUUID())), CallID: id, TargetQuota: target, Delta: delta, State: "pending", CreatedAt: time.Now().Unix()}
		if err = tx.Create(&intent).Error; err != nil {
			return err
		}
		return tx.Model(&call).Update("token_reserved", target).Error
	})
	if err != nil || intent.ID == "" {
		return err
	}
	if err = s.resolveProductTopUp(ctx, intent.ID); err != nil {
		return err
	}
	if intent.TargetQuota < target {
		return s.TopUp(ctx, id, target)
	}
	return nil
}

func (s *ProductFundingStore) Settle(ctx context.Context, id string, actual int, usage string) error {
	if actual < 0 || actual > common.MaxQuota {
		return ErrProductFundingConflict
	}
	if usage == "" {
		usage = "{}"
	}
	var normalized any
	if err := common.UnmarshalJsonStr(usage, &normalized); err != nil {
		return ErrProductFundingConflict
	}
	raw, err := common.Marshal(normalized)
	if err != nil {
		return err
	}
	usage = string(raw)
	digest := productDigest(fmt.Sprintf("%d:%s", actual, usage))
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c ProductRuntimeCall
		if err := lockForUpdate(tx).First(&c, "id = ?", id).Error; err != nil {
			return err
		}
		if c.ScopeDigest == "" || c.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if c.FinalDigest != "" {
			if c.FinalDigest != digest || c.State == "release_pending" || c.State == "released" {
				return ErrProductFundingConflict
			}
			return nil
		}
		if c.State != "reserved" {
			return ErrProductFundingPending
		}
		return tx.Model(&c).Updates(map[string]any{"state": "settle_pending", "final_quota": actual, "final_usage": usage, "final_digest": digest}).Error
	})
	if err != nil {
		return err
	}
	return s.reconcile(ctx, id)
}

func (s *ProductFundingStore) Release(ctx context.Context, id, reason string) error {
	if reason == "" {
		return ErrProductFundingConflict
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c ProductRuntimeCall
		if err := lockForUpdate(tx).First(&c, "id = ?", id).Error; err != nil {
			return err
		}
		if c.ScopeDigest == "" || c.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if c.State == "released" || c.State == "release_pending" {
			return nil
		}
		if c.State != "reserved" {
			return ErrProductFundingPending
		}
		return tx.Model(&c).Updates(map[string]any{"state": "release_pending", "final_quota": 0, "final_usage": reason, "final_digest": productDigest(reason)}).Error
	})
	if err != nil {
		return err
	}
	return s.reconcile(ctx, id)
}

func (s *ProductFundingStore) reconcile(ctx context.Context, id string) error {
	var c ProductRuntimeCall
	if err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return err
	}
	if c.ScopeDigest == "" || c.ScopeDigest != s.ScopeDigest() {
		return ErrProductFundingConflict
	}
	if c.State == "settled" || c.State == "released" {
		return nil
	}
	if c.State != "settle_pending" && c.State != "release_pending" {
		return ErrProductFundingPending
	}
	var remote userkit.RuntimeCall
	var err error
	if c.State == "settle_pending" {
		if err = s.TopUp(ctx, id, c.FinalQuota); err != nil {
			return err
		}
		remote, err = s.client.Settle(ctx, id, userkit.RuntimeSettlementRequest{Amount: strconv.Itoa(c.FinalQuota), PricingDigest: c.PricingDigest, Usage: []byte(c.FinalUsage)})
	} else {
		var topups []ProductFundingTopUp
		if err = s.db.WithContext(ctx).Where("call_id = ? AND state = ?", id, "pending").Find(&topups).Error; err != nil {
			return err
		}
		for _, op := range topups {
			if err = s.resolveProductTopUp(ctx, op.ID); err != nil {
				return err
			}
		}
		remote, err = s.client.Release(ctx, id, userkit.RuntimeReleaseRequest{Reason: c.FinalUsage})
	}
	if err != nil {
		return err
	}
	if err = s.validateRemote(c, remote); err != nil {
		return err
	}
	terminal := "settled"
	if c.State == "release_pending" {
		terminal = "released"
	}
	if strings.ToLower(remote.Status) != terminal || (terminal == "settled" && remote.Charged != strconv.Itoa(c.FinalQuota)) {
		return ErrProductFundingPending
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reserve and cutover lock User before the financial call. Keep that
		// order here too, including duplicate requests settling concurrently.
		var owner User
		if err := lockForUpdate(tx).Unscoped().Select("id").First(&owner, c.UserID).Error; err != nil {
			return err
		}
		var current ProductRuntimeCall
		if err := lockForUpdate(tx).First(&current, "id = ?", id).Error; err != nil {
			return err
		}
		if current.State == terminal {
			return nil
		}
		if current.FinalDigest != c.FinalDigest || current.State != c.State {
			return ErrProductFundingConflict
		}
		if err := productTokenDelta(tx, current.TokenID, current.FinalQuota-current.TokenReserved); err != nil {
			return err
		}
		if terminal == "settled" && !current.ExternalCounters {
			if err := tx.Model(&User{}).Where("id = ?", c.UserID).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", current.FinalQuota), "request_count": gorm.Expr("request_count + 1")}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&current).Updates(map[string]any{"state": terminal, "updated_at": time.Now().Unix()}).Error
	})
	if err == nil {
		_ = InvalidateUserTokensCache(c.UserID)
		_ = invalidateUserCache(c.UserID)
	}
	return err
}

func (s *ProductFundingStore) Recover(ctx context.Context) error {
	var first error
	var topups []ProductFundingTopUp
	if err := s.db.WithContext(ctx).Where("state = ?", "pending").Order("updated_at, id").Find(&topups).Error; err != nil {
		return err
	}
	topupCtx, stopTopups := productRecoveryFirstPhase(ctx)
	for _, op := range topups {
		if topupCtx.Err() != nil {
			break
		}
		if e := s.db.WithContext(topupCtx).Model(&ProductFundingTopUp{}).Where("id = ? AND state = ?", op.ID, "pending").Update("updated_at", time.Now().Unix()).Error; e != nil {
			if first == nil {
				first = e
			}
			continue
		}
		if err := s.resolveProductTopUp(topupCtx, op.ID); err != nil && first == nil {
			first = err
		}
	}
	stopTopups()
	var calls []ProductRuntimeCall
	if err := s.db.WithContext(ctx).Where("state IN ? OR (state IN ? AND dispatched_at = 0 AND financial_commitment = false AND dispatch_lease_until > 0 AND dispatch_lease_until <= ?)", []string{"settle_pending", "release_pending"}, []string{"reserve_pending", "reserved"}, time.Now().Unix()).Order("updated_at, id").Find(&calls).Error; err != nil {
		return err
	}
	for _, call := range calls {
		if ctx.Err() != nil {
			break
		}
		if e := s.db.WithContext(ctx).Model(&ProductRuntimeCall{}).Where("id = ?", call.ID).Update("updated_at", time.Now().Unix()).Error; e != nil {
			if first == nil {
				first = e
			}
			continue
		}
		var err error
		if call.State == "reserve_pending" || call.State == "reserved" {
			err = s.recoverProductReserve(ctx, call.ID)
		} else {
			err = s.reconcile(ctx, call.ID)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func ProductFundingState(ctx context.Context, id int) (string, error) {
	if DB == nil {
		return "local", nil
	}
	if !platformaccount.RuntimeEnabled() && !DB.Migrator().HasTable(&ProductFundingAccount{}) {
		return "local", nil
	}
	var a ProductFundingAccount
	err := DB.WithContext(ctx).First(&a, "user_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var user User
		if e := DB.WithContext(ctx).Select("id", "product_registration_pending").First(&user, id).Error; e != nil {
			return "", e
		}
		if user.ProductRegistrationPending {
			return "provisioning", nil
		}
		return "local", nil
	}
	return a.State, err
}

func EnsureProductFunding(ctx context.Context, id int) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	if err := CompleteProductJITOnboarding(ctx, id); err != nil {
		return err
	}
	s, err := productFundingStore()
	if err != nil {
		return err
	}
	if a, err := s.Account(ctx, id); err == nil {
		if a.State != "central" {
			return ErrProductFundingPending
		}
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	access, err := s.client.Access(ctx, strconv.Itoa(id))
	if err != nil {
		return err
	}
	return s.ActivateZero(ctx, id, access.Binding.PersonalAccountID)
}

func RefreshProductBalance(ctx context.Context, user *User) error {
	state, err := ProductFundingState(ctx, user.Id)
	if err != nil {
		return err
	}
	if state == "local" {
		return nil
	}
	if state != "central" {
		return ErrProductFundingPending
	}
	if err = FlushProductCreditAwards(ctx, user.Id); err != nil {
		return err
	}
	s, err := productFundingStore()
	if err != nil {
		return err
	}
	user.Quota, err = s.Balance(ctx, user.Id)
	return err
}

func ProductFundingStoreForUser(ctx context.Context, id int) (*ProductFundingStore, bool, error) {
	state, err := ProductFundingState(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if state == "local" {
		return nil, false, nil
	}
	if state != "central" {
		return nil, true, ErrProductFundingPending
	}
	s, err := productFundingStore()
	return s, true, err
}

// Receipt creation is shared with native/OCR funding adapters and migrations.
func RegisterProductFundingModels(db *gorm.DB) error {
	return db.AutoMigrate(&ProductFundingAccount{}, &ProductRuntimeCall{}, &ProductLegacyFundingLease{}, &ProductFundingTopUp{})
}

// Explicit table creation is kept separate from account activation. Starting the
// application never imports balances or changes existing funding ownership.

func RecoverProductFunding(ctx context.Context) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	store, err := productFundingStore()
	if err != nil {
		return err
	}
	return store.Recover(ctx)
}

// ScopeDigest excludes the credential so rotation cannot change ownership, but
// fixes origin/product/instance to prevent replay into a different deployment.
func (s *ProductFundingStore) ScopeDigest() string {
	if s.client == nil {
		return ""
	}
	scope := s.client.Scope()
	return productDigest(scope.Origin + "\x00" + scope.ProductID + "\x00" + scope.InstanceID)
}
func ProductRuntimeScopeDigest() (string, error) {
	s, e := productFundingStore()
	if e != nil {
		return "", e
	}
	return s.ScopeDigest(), nil
}
