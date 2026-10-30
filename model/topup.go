package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type TopUp struct {
	Id                int     `json:"id"`
	UserId            int     `json:"user_id" gorm:"index"`
	Amount            int64   `json:"amount"`
	Money             float64 `json:"money"`
	TradeNo           string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod     string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider   string  `json:"payment_provider" gorm:"type:varchar(50);default:'';uniqueIndex:idx_topups_provider_trade"`
	Currency          string  `json:"currency" gorm:"type:varchar(8);default:''"`
	PaidAmountMinor   int64   `json:"paid_amount_minor"`
	CreditedQuota     int     `json:"credited_quota"`
	ProviderTradeNo   *string `json:"provider_trade_no,omitempty" gorm:"type:varchar(64);uniqueIndex:idx_topups_provider_trade"`
	NextReconcileTime int64   `json:"next_reconcile_time" gorm:"index"`
	ReconcileAttempts int     `json:"reconcile_attempts"`
	CreateTime        int64   `json:"create_time"`
	CompleteTime      int64   `json:"complete_time"`
	Status            string  `json:"status"`
	CreditStatus      string  `json:"credit_status,omitempty" gorm:"-"`
}

const (
	PaymentMethodStripe                   = "stripe"
	PaymentMethodCreem                    = "creem"
	PaymentMethodWaffo                    = "waffo"
	PaymentMethodWaffoPancake             = "waffo_pancake"
	PaymentMethodBalance                  = "balance"
	PaymentMethodWeChatNative             = "wechat_native"
	WeChatTopUpOrderLifetimeSeconds int64 = 15 * 60
)

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderBalance      = "balance"
	PaymentProviderWeChatPay    = "wechat_pay"
)

var (
	ErrPaymentMethodMismatch      = errors.New("payment method mismatch")
	ErrTopUpNotFound              = errors.New("topup not found")
	ErrTopUpStatusInvalid         = errors.New("topup status invalid")
	ErrInvalidTopUpQuota          = errors.New("invalid top-up quota")
	ErrTopUpQuotaLimitExceeded    = errors.New("top-up quota limit exceeded")
	ErrTopUpOrderConflict         = errors.New("top-up order conflicts with existing snapshot")
	ErrTopUpAmountMismatch        = errors.New("top-up amount mismatch")
	ErrTopUpCurrencyMismatch      = errors.New("top-up currency mismatch")
	ErrTopUpProviderTradeConflict = errors.New("provider transaction belongs to another top-up")
)

type WeChatTopUpOrderInput struct {
	UserID        int
	TradeNo       string
	AmountMinor   int64
	CreditedQuota int
	CreateTime    int64
}

type WeChatTopUpSettlement struct {
	TradeNo         string
	ProviderTradeNo string
	AmountMinor     int64
	Currency        string
	CallerIP        string
}

type WeChatPayIncident struct {
	UserID          int
	EventID         string
	EventType       string
	TradeNo         string
	ProviderTradeNo string
	Reason          string
	RequestID       string
	CallerIP        string
	Source          string
}

func RecordWeChatPayIncident(incident WeChatPayIncident) error {
	adminInfo := map[string]interface{}{
		"event_id":          incident.EventID,
		"event_type":        incident.EventType,
		"trade_no":          incident.TradeNo,
		"provider_trade_no": incident.ProviderTradeNo,
		"reason":            incident.Reason,
		"request_id":        incident.RequestID,
		"caller_ip":         incident.CallerIP,
		"source":            incident.Source,
	}
	other, err := common.Marshal(map[string]interface{}{"admin_info": adminInfo})
	if err != nil {
		return err
	}
	username := ""
	if incident.UserID > 0 {
		username, _ = GetUsernameById(incident.UserID, true)
	}
	content := "WeChat Pay callback requires reconciliation"
	if incident.Source == "reconciliation" {
		content = "WeChat Pay reconciliation requires review"
	}
	return createLog(&Log{
		UserId:    incident.UserID,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeTopup,
		Content:   content,
		Ip:        incident.CallerIP,
		RequestId: incident.RequestID,
		Other:     string(other),
	})
}

func (topUp *TopUp) Insert() error {
	var err error
	err = DB.Create(topUp).Error
	return err
}

func CreateOrReuseWeChatTopUp(input WeChatTopUpOrderInput) (*TopUp, bool, error) {
	if input.UserID <= 0 || input.TradeNo == "" || input.AmountMinor <= 0 || input.CreditedQuota <= 0 || input.CreateTime <= 0 {
		return nil, false, errors.New("invalid WeChat top-up order")
	}

	validateExisting := func(existing *TopUp) error {
		if existing.UserId != input.UserID ||
			existing.PaymentProvider != PaymentProviderWeChatPay ||
			existing.PaymentMethod != PaymentMethodWeChatNative ||
			existing.Currency != "CNY" ||
			existing.PaidAmountMinor != input.AmountMinor ||
			existing.CreditedQuota != input.CreditedQuota {
			return ErrTopUpOrderConflict
		}
		return nil
	}

	existing := &TopUp{}
	if err := DB.Where("trade_no = ?", input.TradeNo).First(existing).Error; err == nil {
		if err := validateExisting(existing); err != nil {
			return nil, false, err
		}
		return existing, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	if err := ValidateTopUpQuotaCapacity(input.UserID, input.CreditedQuota); err != nil {
		return nil, false, err
	}

	topUp := &TopUp{
		UserId:            input.UserID,
		Money:             float64(input.AmountMinor) / 100,
		TradeNo:           input.TradeNo,
		PaymentMethod:     PaymentMethodWeChatNative,
		PaymentProvider:   PaymentProviderWeChatPay,
		Currency:          "CNY",
		PaidAmountMinor:   input.AmountMinor,
		CreditedQuota:     input.CreditedQuota,
		NextReconcileTime: input.CreateTime + 30,
		CreateTime:        input.CreateTime,
		Status:            common.TopUpStatusPending,
	}
	if err := DB.Create(topUp).Error; err == nil {
		return topUp, false, nil
	} else {
		createErr := err
		winner := &TopUp{}
		if loadErr := DB.Where("trade_no = ?", input.TradeNo).First(winner).Error; loadErr != nil {
			return nil, false, createErr
		}
		if validateErr := validateExisting(winner); validateErr != nil {
			return nil, false, validateErr
		}
		return winner, true, nil
	}
}

func SettleWeChatTopUp(settlement WeChatTopUpSettlement) (
	alreadyDone bool,
	userID int,
	creditedQuota int,
	err error,
) {
	if settlement.TradeNo == "" || settlement.ProviderTradeNo == "" {
		return false, 0, 0, errors.New("invalid WeChat top-up settlement")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	topUp := &TopUp{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", settlement.TradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderWeChatPay || topUp.PaymentMethod != PaymentMethodWeChatNative {
			return ErrPaymentMethodMismatch
		}
		if topUp.PaidAmountMinor != settlement.AmountMinor {
			return ErrTopUpAmountMismatch
		}
		if topUp.Currency != "CNY" || settlement.Currency != topUp.Currency {
			return ErrTopUpCurrencyMismatch
		}
		if topUp.ProviderTradeNo != nil && *topUp.ProviderTradeNo != settlement.ProviderTradeNo {
			return ErrTopUpProviderTradeConflict
		}

		var conflictingCount int64
		if err := tx.Model(&TopUp{}).
			Where("payment_provider = ? AND provider_trade_no = ? AND trade_no <> ?", PaymentProviderWeChatPay, settlement.ProviderTradeNo, settlement.TradeNo).
			Count(&conflictingCount).Error; err != nil {
			return err
		}
		if conflictingCount != 0 {
			return ErrTopUpProviderTradeConflict
		}

		userID = topUp.UserId
		creditedQuota = topUp.CreditedQuota
		if topUp.Status == common.TopUpStatusSuccess {
			alreadyDone = true
			if topUp.ProviderTradeNo == nil {
				topUp.ProviderTradeNo = &settlement.ProviderTradeNo
				if err := tx.Save(topUp).Error; err != nil {
					return err
				}
			}
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.ProviderTradeNo = &settlement.ProviderTradeNo
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		return creditTopUpQuota(tx, topUp.UserId, topUp.CreditedQuota, nil, topUp)
	})
	if err != nil {
		return false, 0, 0, err
	}
	if alreadyDone {
		syncProductCreditResult(userID, 0, "WeChat Pay replay")
		return true, userID, creditedQuota, nil
	}

	syncProductCreditResult(userID, creditedQuota, "WeChat Pay topup")
	RecordTopupLog(
		userID,
		fmt.Sprintf("微信支付充值成功，充值额度: %v，支付金额：%s %s", logger.FormatQuota(creditedQuota), formatTopUpMinor(topUp.PaidAmountMinor), topUp.Currency),
		settlement.CallerIP,
		PaymentMethodWeChatNative,
		PaymentProviderWeChatPay,
	)
	return false, userID, creditedQuota, nil
}

func formatTopUpMinor(amountMinor int64) string {
	return decimal.NewFromInt(amountMinor).Shift(-2).StringFixed(2)
}

func GetUserWeChatTopUp(userID int, tradeNo string) (*TopUp, error) {
	if userID <= 0 || tradeNo == "" {
		return nil, ErrTopUpNotFound
	}
	topUp := &TopUp{}
	result := DB.Where(
		"user_id = ? AND trade_no = ? AND payment_provider = ?",
		userID,
		tradeNo,
		PaymentProviderWeChatPay,
	).First(topUp)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ErrTopUpNotFound
		}
		return nil, result.Error
	}
	return topUp, nil
}

func ExpireWeChatTopUp(tradeNo string) error {
	if tradeNo == "" {
		return ErrTopUpNotFound
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderWeChatPay {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		return tx.Model(topUp).Updates(map[string]interface{}{
			"status":              common.TopUpStatusExpired,
			"complete_time":       common.GetTimestamp(),
			"next_reconcile_time": 0,
		}).Error
	})
}

func HasDueWeChatTopUps(now int64) bool {
	if now <= 0 {
		return false
	}
	var count int64
	err := DB.Model(&TopUp{}).Where(
		"payment_provider = ? AND status = ? AND next_reconcile_time > 0 AND next_reconcile_time <= ?",
		PaymentProviderWeChatPay,
		common.TopUpStatusPending,
		now,
	).Limit(1).Count(&count).Error
	if err != nil {
		common.SysError("check due WeChat top-ups failed: " + err.Error())
		return false
	}
	return count != 0
}

func ClaimDueWeChatTopUps(now int64, limit int) ([]*TopUp, error) {
	if now <= 0 || limit <= 0 {
		return nil, errors.New("invalid WeChat top-up claim arguments")
	}

	claimed := make([]*TopUp, 0, limit)
	err := DB.Transaction(func(tx *gorm.DB) error {
		candidates := make([]*TopUp, 0, limit)
		if err := tx.Where(
			"payment_provider = ? AND status = ? AND next_reconcile_time > 0 AND next_reconcile_time <= ?",
			PaymentProviderWeChatPay,
			common.TopUpStatusPending,
			now,
		).Order("next_reconcile_time ASC, id ASC").Limit(limit).Find(&candidates).Error; err != nil {
			return err
		}

		for _, candidate := range candidates {
			nextReconcileTime := nextWeChatReconcileTime(candidate, now)
			result := tx.Model(&TopUp{}).
				Where(
					"id = ? AND payment_provider = ? AND status = ? AND next_reconcile_time > 0 AND next_reconcile_time <= ?",
					candidate.Id,
					PaymentProviderWeChatPay,
					common.TopUpStatusPending,
					now,
				).
				Updates(map[string]interface{}{
					"reconcile_attempts":  gorm.Expr("reconcile_attempts + 1"),
					"next_reconcile_time": nextReconcileTime,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			candidate.ReconcileAttempts++
			candidate.NextReconcileTime = nextReconcileTime
			claimed = append(claimed, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func nextWeChatReconcileTime(topUp *TopUp, now int64) int64 {
	offsets := [...]int64{60, 180, 300, 600, 900}
	index := topUp.ReconcileAttempts
	if index < 0 {
		index = 0
	}
	if index >= len(offsets) {
		index = len(offsets) - 1
	}
	next := topUp.CreateTime + offsets[index]
	if next <= now {
		if now >= topUp.CreateTime+WeChatTopUpOrderLifetimeSeconds {
			return now + 10*60
		}
		return now + 30
	}
	return next
}

func topUpQuotaMaxCurrent(creditedQuota int) (int, error) {
	if creditedQuota <= 0 || creditedQuota >= common.MaxQuota {
		return 0, ErrInvalidTopUpQuota
	}
	return common.MaxQuota - 1 - creditedQuota, nil
}

// ValidateTopUpQuotaCapacity performs the user-facing pre-payment check. The
// settlement path repeats the same invariant with an atomic conditional
// update, because the wallet balance can change after checkout creation.
func ValidateTopUpQuotaCapacity(userId int, creditedQuota int) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	quota, err := GetUserQuota(userId, true)
	if err != nil {
		return err
	}
	if quota > maxCurrentQuota {
		return ErrTopUpQuotaLimitExceeded
	}
	return nil
}

// creditTopUpQuota atomically enforces the int32 wallet ceiling while adding
// quota. Keeping the predicate and increment in one UPDATE prevents two
// concurrent callbacks from both passing a separate read/check.
func creditTopUpQuota(tx *gorm.DB, userId int, creditedQuota int, updates map[string]interface{}, order *TopUp) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	if order == nil || order.UserId != userId || order.TradeNo == "" || order.Status != common.TopUpStatusSuccess {
		return ErrTopUpOrderConflict
	}
	// Match cutover's user -> funding-account lock order, including customer
	// metadata writes that would otherwise acquire the user after the account.
	var owner User
	if err := lockForUpdate(tx).Select("id").First(&owner, userId).Error; err != nil {
		return err
	}
	handled, err := QueueProductCreditAward(tx, userId, "payment", productDigest(order.PaymentProvider+":"+order.TradeNo), creditedQuota, map[string]any{
		"order_id": order.Id, "trade_no": order.TradeNo, "provider": order.PaymentProvider,
		"provider_trade_no": order.ProviderTradeNo, "currency": order.Currency,
		"paid_amount_minor": order.PaidAmountMinor, "money": decimal.NewFromFloat(order.Money).String(), "credited_quota": creditedQuota,
	})
	if err != nil {
		return err
	}
	if handled {
		if len(updates) > 0 {
			return tx.Model(&User{}).Where("id = ?", userId).Updates(updates).Error
		}
		return nil
	}

	updateFields := make(map[string]interface{}, len(updates)+1)
	for key, value := range updates {
		updateFields[key] = value
	}
	updateFields["quota"] = gorm.Expr("quota + ?", creditedQuota)

	result := tx.Model(&User{}).
		Where("id = ? AND quota <= ?", userId, maxCurrentQuota).
		Updates(updateFields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var count int64
	if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrTopUpQuotaLimitExceeded
}

func (topUp *TopUp) Update() error {
	var err error
	err = DB.Save(topUp).Error
	return err
}

func GetTopUpById(id int) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("id = ?", id).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	topUp, err := GetTopUpByTradeNoWithError(tradeNo)
	if err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNoWithError(tradeNo string) (*TopUp, error) {
	if tradeNo == "" {
		return nil, ErrTopUpNotFound
	}
	topUp := &TopUp{}
	if err := DB.Where("trade_no = ?", tradeNo).First(topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTopUpNotFound
		}
		return nil, err
	}
	return topUp, nil
}

func UpdatePendingTopUpStatus(tradeNo string, expectedPaymentProvider string, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if expectedPaymentProvider != "" && topUp.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.Status = targetStatus
		return tx.Save(topUp).Error
	})
}

// RechargeEpay 原子完成易支付订单：订单行锁、状态校验、成功更新与用户额度增加
// 在同一个事务内完成，因此同一订单的并发/重复回调（包括多实例部署下）最多充值一次。
// alreadyDone=true 表示订单此前已完成，本次为幂等重复回调。
// 进程内的 LockOrder 只是优化，正确性由本函数的数据库行锁保证。
func RechargeEpay(tradeNo string, actualPaymentMethod string, callerIp string) (alreadyDone bool, err error) {
	if tradeNo == "" {
		return false, errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var quotaToAdd int
	topUp := &TopUp{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderEpay {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			alreadyDone = true
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		if actualPaymentMethod != "" && topUp.PaymentMethod != actualPaymentMethod {
			topUp.PaymentMethod = actualPaymentMethod
		}
		var quotaErr error
		quotaToAdd, quotaErr = common.QuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil, topUp)
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) && !errors.Is(err, ErrPaymentMethodMismatch) && !errors.Is(err, ErrTopUpStatusInvalid) {
			common.SysError("epay topup failed: " + err.Error())
		}
		return false, err
	}
	if alreadyDone {
		syncProductCreditResult(topUp.UserId, 0, "epay replay")
		return true, nil
	}
	syncProductCreditResult(topUp.UserId, quotaToAdd, "epay topup")

	common.SysLog(fmt.Sprintf("易支付充值成功 trade_no=%s user_id=%d quota_to_add=%d money=%.2f", topUp.TradeNo, topUp.UserId, quotaToAdd, topUp.Money))
	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f", logger.LogQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentProviderEpay)
	return false, nil
}

func Recharge(referenceId string, customerId string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("未提供支付单号")
	}

	var quota int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderStripe {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		quota, err = common.QuotaFromDecimalStrict(
			decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}
		return creditTopUpQuota(tx, topUp.UserId, quota, map[string]interface{}{
			"stripe_customer": customerId,
		}, topUp)
	})

	if err != nil {
		common.SysError("topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncProductCreditResult(topUp.UserId, quota, "stripe topup")

	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%d", logger.FormatQuota(quota), topUp.Amount), callerIp, topUp.PaymentMethod, PaymentMethodStripe)

	return nil
}

// topUpQueryWindowSeconds 限制充值记录查询的时间窗口（秒）。
const topUpQueryWindowSeconds int64 = 30 * 24 * 60 * 60

// topUpQueryCutoff 返回允许查询的最早 create_time（秒级 Unix 时间戳）。
func topUpQueryCutoff() int64 {
	return common.GetTimestamp() - topUpQueryWindowSeconds
}

func GetUserTopUps(userId int, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	cutoff := topUpQueryCutoff()

	// Get total count within transaction
	err = tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, cutoff).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated topups within same transaction
	err = tx.Where("user_id = ? AND create_time >= ?", userId, cutoff).Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// GetAllTopUps 获取全平台的充值记录（管理员使用，不限制时间窗口）
func GetAllTopUps(pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err = tx.Model(&TopUp{}).Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// searchTopUpCountHardLimit 搜索充值记录时 COUNT 的安全上限，
// 防止对超大表执行无界 COUNT 触发 DoS。
const searchTopUpCountHardLimit = 10000

// SearchUserTopUps 按订单号搜索某用户的充值记录
func SearchUserTopUps(userId int, keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, topUpQueryCutoff())
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// SearchAllTopUps 按订单号搜索全平台充值记录（管理员使用，不限制时间窗口）
func SearchAllTopUps(keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// ManualCompleteTopUp 管理员手动完成订单并给用户充值
func ManualCompleteTopUp(tradeNo string, callerIp string, realm ...string) error {
	if tradeNo == "" {
		return errors.New("未提供订单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var userId int
	var quotaToAdd int
	var payMoney float64
	var paymentMethod string

	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		// 行级锁，避免并发补单
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return errors.New("充值订单不存在")
		}

		if err := lockFundingManagementOwnerTx(tx, topUp.UserId, realm); err != nil {
			return err
		}

		// 幂等处理：已成功直接返回
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("订单状态不是待支付，无法补单")
		}

		// 计算应充值额度：
		// - 微信订单：只使用创建时的精确额度快照，不按当前汇率或单价重算
		// - Stripe 订单：Money 代表经分组倍率换算后的美元数量，直接 * QuotaPerUnit
		// - 其他订单（如易支付）：Amount 为美元数量，* QuotaPerUnit
		var quotaErr error
		if topUp.PaymentProvider == PaymentProviderWeChatPay {
			quotaToAdd = topUp.CreditedQuota
			if quotaToAdd <= 0 {
				return ErrInvalidTopUpQuota
			}
		} else if topUp.PaymentProvider == PaymentProviderStripe {
			quotaToAdd, quotaErr = common.QuotaFromDecimalStrict(
				decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
			)
		} else {
			quotaToAdd, quotaErr = common.QuotaFromDecimalStrict(
				decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
			)
		}
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		// 标记完成
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		// 增加用户额度（立即写库，保持一致性）
		if err := creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil, topUp); err != nil {
			return err
		}

		userId = topUp.UserId
		payMoney = topUp.Money
		paymentMethod = topUp.PaymentMethod
		return nil
	})

	if err != nil {
		return err
	}

	// 事务外记录日志，避免阻塞
	syncProductCreditResult(userId, quotaToAdd, "manual topup")
	RecordTopupLog(userId, fmt.Sprintf("管理员补单成功，充值金额: %v，支付金额：%f", logger.FormatQuota(quotaToAdd), payMoney), callerIp, paymentMethod, "admin")
	return nil
}
func RechargeCreem(referenceId string, customerEmail string, customerName string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("未提供支付单号")
	}

	var quota int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderCreem {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		// Creem 直接使用 Amount 作为充值额度（整数）
		quota, err = common.QuotaFromDecimalStrict(decimal.NewFromInt(topUp.Amount))
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}

		// 构建更新字段，优先使用邮箱，如果邮箱为空则使用用户名
		updateFields := map[string]interface{}{}

		// 如果有客户邮箱，尝试更新用户邮箱（仅当用户邮箱为空时）
		if customerEmail != "" {
			// 先检查用户当前邮箱是否为空
			var user User
			err = tx.Where("id = ?", topUp.UserId).First(&user).Error
			if err != nil {
				return err
			}

			// 如果用户邮箱为空，则更新为支付时使用的邮箱
			if user.Email == "" {
				updateFields["email"] = customerEmail
			}
		}

		return creditTopUpQuota(tx, topUp.UserId, quota, updateFields, topUp)
	})

	if err != nil {
		common.SysError("creem topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncProductCreditResult(topUp.UserId, quota, "creem topup")

	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用Creem充值成功，充值额度: %v，支付金额：%.2f", quota, topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodCreem)

	return nil
}

func RechargeWaffo(tradeNo string, callerIp string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffo {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil // 幂等：已成功直接返回
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		quotaToAdd, err = common.QuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil, topUp)
	})

	if err != nil {
		common.SysError("waffo topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncProductCreditResult(topUp.UserId, quotaToAdd, "waffo topup")

	if quotaToAdd > 0 {
		RecordTopupLog(topUp.UserId, fmt.Sprintf("Waffo充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodWaffo)
	}

	return nil
}

func RechargeWaffoPancake(tradeNo string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffoPancake {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		quotaToAdd, err = common.QuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil, topUp)
	})

	if err != nil {
		common.SysError("waffo pancake topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncProductCreditResult(topUp.UserId, quotaToAdd, "waffo pancake topup")

	if quotaToAdd > 0 {
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Waffo Pancake充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money))
	}

	return nil
}

// syncProductCreditResult acknowledges the original receipt once. A central
// transport failure leaves the durable award pending; it must not credit the
// local cache or cause the verified payment to be collected a second time.
func syncProductCreditResult(userID, quota int, operation string) {
	state, err := ProductFundingState(context.Background(), userID)
	if err != nil {
		common.SysError("product credit state pending reconciliation")
		return
	}
	if state != "local" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := FlushProductCreditAwards(ctx, userID); err != nil {
			common.SysError("product credit pending central acknowledgement")
		}
		return
	}
	syncCreditUserQuotaCache(userID, quota, operation)
}
