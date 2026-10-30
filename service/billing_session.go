package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	nativeQuotaSessionID    string
	nativeFundingRefunded   bool
	nativeSettlementQuota   int
	nativeSettlementStarted bool
	legacyLeaseID           string
	relayInfo               *relaycommon.RelayInfo
	funding                 FundingSource
	preConsumedQuota        int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed           int  // 令牌额度实际扣减量
	extraReserved           int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	trusted                 bool // 是否命中信任额度旁路
	fundingSettled          bool // funding.Settle 已成功，资金来源已提交
	settled                 bool // Settle 全部完成（资金 + 令牌）
	refunded                bool // Refund 已调用
	mu                      sync.Mutex
	violationMu             sync.Mutex
	violationAttempted      bool
	violationSucceeded      bool
	reconciliationPending   bool
}

// Settle 根据实际消耗额度进行结算。
// 资金来源和令牌额度分两步提交：若资金来源已提交但令牌调整失败，
// 会标记 fundingSettled 防止 Refund 对已提交的资金来源执行退款。
func (s *BillingSession) Settle(actualQuota int) error {
	if s.relayInfo != nil && s.relayInfo.PlatformMode {
		return errors.New("platform calls cannot use local billing session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleLocked(actualQuota)
}

func (s *BillingSession) settleViolationFee(actualQuota int) error {
	if s.relayInfo != nil && s.relayInfo.PlatformMode {
		return errors.New("platform calls cannot use local billing session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || s.fundingSettled {
		return errors.New("violation fee reservation is no longer active")
	}
	return s.settleLocked(actualQuota)
}

func (s *BillingSession) settleLocked(actualQuota int) error {
	if s.usesNativeTokenQuota() {
		return s.settleNativeLocked(actualQuota)
	}
	if s.reconciliationPending {
		return errors.New("billing reservation requires reconciliation")
	}
	if s.settled {
		return nil
	}
	if s.refunded {
		return errors.New("billing reservation has been refunded")
	}
	delta := actualQuota - s.preConsumedQuota
	if delta == 0 {
		s.settled = true
		return s.finishLegacyLease("settled")
	}
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	if !s.fundingSettled {
		if err := s.funding.Settle(delta); err != nil {
			return err
		}
		s.fundingSettled = true
	}
	// 2) 调整令牌额度
	var tokenErr error
	if !s.relayInfo.IsPlayground {
		if delta > 0 {
			tokenErr = model.DecreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, delta)
		} else {
			tokenErr = model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, -delta)
		}
		if tokenErr != nil {
			// 资金来源已提交，令牌调整失败只能记录日志；标记 settled 防止 Refund 误退资金
			common.SysLog(fmt.Sprintf("error adjusting token quota after funding settled (userId=%d, tokenId=%d, delta=%d): %s",
				s.relayInfo.UserId, s.relayInfo.TokenId, delta, tokenErr.Error()))
		}
	}
	// 3) 更新 relayInfo 上的订阅 PostDelta（用于日志）
	if s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.settled = true
	if tokenErr == nil {
		return s.finishLegacyLease("settled")
	}
	return tokenErr
}

// Refund 退还所有预扣费，幂等安全，异步执行。
func (s *BillingSession) Refund(c *gin.Context) {
	if s.usesNativeTokenQuota() {
		s.refundNative(c)
		return
	}
	if s.relayInfo != nil && s.relayInfo.PlatformMode {
		return
	}
	s.mu.Lock()
	if s.settled || s.refunded || s.fundingSettled || s.reconciliationPending {
		s.mu.Unlock()
		return
	}
	if !s.needsRefundLocked() {
		// A zero/trusted pre-consume owns a migration lease but no money.
		// Close its failed admission without manufacturing a wallet credit.
		s.refunded = true
		s.mu.Unlock()
		if err := s.finishLegacyLease("refunded"); err != nil {
			common.SysError("legacy empty admission receipt pending")
		}
		return
	}
	s.refunded = true
	s.mu.Unlock()

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId,
		logger.FormatQuota(s.tokenConsumed),
		s.funding.Source(),
	))

	// 复制需要的值到闭包中
	tokenId := s.relayInfo.TokenId
	tokenKey := s.relayInfo.TokenKey
	isPlayground := s.relayInfo.IsPlayground
	tokenConsumed := s.tokenConsumed
	extraReserved := s.extraReserved
	subscriptionId := s.relayInfo.SubscriptionId
	funding := s.funding
	leaseID := s.legacyLeaseID

	gopool.Go(func() {
		// 1) 退还资金来源
		if err := funding.Refund(); err != nil {
			common.SysLog("error refunding billing source: " + err.Error())
			return
		}
		extraRefundFailed := false
		if extraReserved > 0 && funding.Source() == BillingSourceSubscription && subscriptionId > 0 {
			if err := model.PostConsumeUserSubscriptionDelta(subscriptionId, -int64(extraReserved)); err != nil {
				common.SysLog("error refunding subscription extra reserved quota: " + err.Error())
				extraRefundFailed = true
			}
		}
		// 2) 退还令牌额度
		if tokenConsumed > 0 && !isPlayground {
			if err := model.IncreaseTokenQuota(tokenId, tokenKey, tokenConsumed); err != nil {
				common.SysLog("error refunding token quota: " + err.Error())
				return
			}
		}
		if leaseID != "" && !extraRefundFailed {
			if err := model.FinishProductLegacyFundingLease(context.Background(), leaseID, "refunded"); err != nil {
				common.SysError("legacy funding refund receipt pending")
			}
		}
	})
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.settled || s.refunded || s.fundingSettled {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.tokenConsumed > 0 {
		return true
	}
	// 订阅可能在 tokenConsumed=0 时仍预扣了额度
	if sub, ok := s.funding.(*SubscriptionFunding); ok && sub.preConsumed > 0 {
		return true
	}
	return false
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	if s.relayInfo != nil && s.relayInfo.PlatformMode {
		return errors.New("platform calls cannot use local billing session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.usesNativeTokenQuota() {
		return s.reserveNativeLocked(targetQuota)
	}
	if s.reconciliationPending {
		return errors.New("billing reservation requires reconciliation")
	}
	if s.settled || s.refunded || targetQuota <= s.preConsumedQuota {
		return nil
	}

	delta := targetQuota - s.preConsumedQuota
	if delta <= 0 {
		return nil
	}

	if err := s.reserveFunding(delta); err != nil {
		return err
	}
	if err := s.reserveToken(delta); err != nil {
		if rollbackErr := s.rollbackFundingReserve(delta); rollbackErr != nil {
			if platformaccount.RuntimeEnabled() && !s.relayInfo.NativeRealm {
				s.reconciliationPending = true
			} else {
				// The failed rollback has an uncertain outcome. Refund only the
				// previously confirmed hold; never credit the uncertain delta.
				if wallet, ok := s.funding.(*WalletFunding); ok {
					wallet.consumed -= delta
				}
			}
			return fmt.Errorf("funding reserve rollback requires reconciliation: %w", rollbackErr)
		}
		return err
	}

	s.preConsumedQuota += delta
	s.tokenConsumed += delta
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口（含信任额度旁路）
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：信任检查 -> 令牌预扣 -> 资金来源预扣。
// 任一步骤失败时原子回滚已完成的步骤。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	if s.usesNativeTokenQuota() {
		return s.preConsumeNative(c, quota)
	}
	if platformaccount.RuntimeEnabled() && !s.relayInfo.NativeRealm && (s.funding.Source() == BillingSourceWallet || s.relayInfo.NativeLocalLegacy) && s.legacyLeaseID == "" {
		id := s.relayInfo.RequestId
		if id == "" {
			id = common.GetUUID()
		}
		if s.relayInfo.NativeLocalLegacy {
			digest := sha256.Sum256([]byte(id))
			id = fmt.Sprintf("native:%x:%s", digest, s.funding.Source())
		}
		var err error
		if s.relayInfo.NativeLocalLegacy {
			err = model.BeginUnboundNativeFundingLease(context.Background(), id, s.relayInfo.UserId)
		} else {
			err = model.BeginProductLegacyFundingLease(context.Background(), id, s.relayInfo.UserId)
		}
		if err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		s.legacyLeaseID = id
	}
	effectiveQuota := quota

	// ---- 信任额度旁路 ----
	if s.shouldTrust(c) {
		s.trusted = true
		effectiveQuota = 0
		logger.LogInfo(c, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.relayInfo.UserId, s.funding.Source()))
	} else if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	// ---- 1) 预扣令牌额度 ----
	if effectiveQuota > 0 {
		if err := PreConsumeTokenQuota(s.relayInfo, effectiveQuota); err != nil {
			if errors.Is(err, ErrInsufficientTokenQuota) {
				if receiptErr := s.finishLegacyLease("refunded"); receiptErr != nil {
					return types.NewError(receiptErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
				}
			}
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		s.tokenConsumed = effectiveQuota
	}

	// ---- 2) 预扣资金来源 ----
	if err := s.funding.PreConsume(effectiveQuota); err != nil {
		// 预扣费失败，回滚令牌额度
		if s.tokenConsumed > 0 && !s.relayInfo.IsPlayground {
			if rollbackErr := model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.tokenConsumed); rollbackErr != nil {
				common.SysLog(fmt.Sprintf("error rolling back token quota (userId=%d, tokenId=%d, amount=%d, fundingErr=%s): %s",
					s.relayInfo.UserId, s.relayInfo.TokenId, s.tokenConsumed, err.Error(), rollbackErr.Error()))
				// Keep the migration fence and the unresolved debit. In particular,
				// wallet_first must not start another funding path after this error.
				return types.NewError(fmt.Errorf("token rollback pending after funding failure: %v: %w", err, rollbackErr), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			s.tokenConsumed = 0
		}
		// TODO: model 层应定义哨兵错误（如 ErrNoActiveSubscription），用 errors.Is 替代字符串匹配
		if errors.Is(err, ErrInsufficientWalletQuota) {
			if receiptErr := s.finishLegacyLease("refunded"); receiptErr != nil {
				return types.NewError(receiptErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			return insufficientWalletQuotaError(s.relayInfo.UserId)
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
			if receiptErr := s.finishLegacyLease("refunded"); receiptErr != nil {
				return types.NewError(receiptErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

func (s *BillingSession) reserveFunding(delta int) error {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		reserved, err := model.TryReserveUserQuota(funding.userId, delta)
		if err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		if !reserved {
			return insufficientWalletQuotaError(funding.userId)
		}
		funding.consumed += delta
		return nil
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, int64(delta)); err != nil {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("订阅额度不足或未配置订阅: %s", err.Error()),
				types.ErrorCodeInsufficientUserQuota,
				http.StatusForbidden,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
		return nil
	default:
		return types.NewError(fmt.Errorf("unsupported funding source: %s", s.funding.Source()), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
}

// Preserve the existing user-facing message while carrying the definite
// no-debit classification through NewAPIError.Unwrap.
type walletQuotaDeclinedError struct{ message string }

func (e *walletQuotaDeclinedError) Error() string { return e.message }
func (e *walletQuotaDeclinedError) Unwrap() error { return ErrInsufficientWalletQuota }

func insufficientWalletQuotaError(userId int) *types.NewAPIError {
	userQuota, err := model.GetUserQuota(userId, false)
	if err != nil {
		userQuota = 0
	}
	return types.NewErrorWithStatusCode(
		&walletQuotaDeclinedError{message: fmt.Sprintf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota))},
		types.ErrorCodeInsufficientUserQuota,
		http.StatusForbidden,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithNoRecordErrorLog(),
	)
}

func (s *BillingSession) rollbackFundingReserve(delta int) error {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		if err := model.IncreaseUserQuota(funding.userId, delta, false); err != nil {
			common.SysLog("error rolling back wallet funding reserve: " + err.Error())
			return err
		} else {
			funding.consumed -= delta
		}
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, -int64(delta)); err != nil {
			common.SysLog("error rolling back subscription funding reserve: " + err.Error())
			return err
		}
	}
	return nil
}

func (s *BillingSession) reserveToken(delta int) error {
	if delta <= 0 || s.relayInfo.IsPlayground {
		return nil
	}
	if err := PreConsumeTokenQuota(s.relayInfo, delta); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}

// shouldTrust 统一信任额度检查，适用于钱包和订阅。
func (s *BillingSession) shouldTrust(c *gin.Context) bool {
	// 异步任务（ForcePreConsume=true）必须预扣全额，不允许信任旁路
	if s.relayInfo.ForcePreConsume {
		return false
	}

	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}

	// 检查令牌是否充足
	tokenTrusted := s.relayInfo.TokenUnlimited
	if !tokenTrusted {
		tokenQuota := c.GetInt("token_quota")
		tokenTrusted = tokenQuota > trustQuota
	}
	if !tokenTrusted {
		return false
	}

	switch s.funding.Source() {
	case BillingSourceWallet:
		return s.relayInfo.UserQuota > trustQuota
	case BillingSourceSubscription:
		// 订阅不能启用信任旁路。原因：
		// 1. PreConsumeUserSubscription 要求 amount>0 来创建预扣记录并锁定订阅
		// 2. SubscriptionFunding.PreConsume 忽略参数，始终用 s.amount 预扣
		// 3. 若信任旁路将 effectiveQuota 设为 0，会导致 preConsumedQuota 与实际订阅预扣不一致
		return false
	default:
		return false
	}
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 根据计费偏好创建会话并处理回退
// ---------------------------------------------------------------------------

// NewBillingSession 根据用户计费偏好创建 BillingSession，处理 subscription_first / wallet_first 的回退。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (relaycommon.BillingSettler, *types.NewAPIError) {
	if relayInfo == nil || relayInfo.PlatformMode {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if relayInfo.NativeRealm {
		user, err := model.NativeRealmUser(c.Request.Context(), relayInfo.UserId)
		if err != nil || user.Status != common.UserStatusEnabled {
			return nil, types.NewError(model.ErrNativeCredentialUnverified, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if relayInfo.NativeRealm && relayInfo.IsPlayground && relayInfo.TokenId == 0 {
		// A dashboard call has no API key. Recheck the authenticated persisted
		// SID rather than deriving authority from the playground/native flags.
		value, present := c.Get("auth_identity")
		identity, valid := value.(AuthIdentity)
		native, err := IsNativeLoginSession(ctx, identity)
		if !present || !valid || identity.UserID != relayInfo.UserId || err != nil || !native {
			return nil, types.NewError(model.ErrNativeCredentialUnverified, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
	} else if err := model.ValidateLocalFundingCredential(ctx, relayInfo.UserId, relayInfo.TokenId); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	pref := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)

	// 钱包路径需要先检查用户额度
	tryWallet := func() (relaycommon.BillingSettler, *types.NewAPIError) {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		if !relayInfo.NativeLocalLegacy && !relayInfo.NativeRealm {
			central, managed, e := newProductFundingSession(ctx, relayInfo, preConsumedQuota)
			if e != nil {
				var failure *userkit.RuntimeError
				if errors.As(e, &failure) && failure.Status == 402 {
					return nil, types.NewErrorWithStatusCode(e, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
				}
				return nil, types.NewError(e, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			if managed {
				return central, nil
			}
		}

		userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if userQuota <= 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if userQuota-preConsumedQuota < 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(preConsumedQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		relayInfo.UserQuota = userQuota

		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &WalletFunding{userId: relayInfo.UserId},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (relaycommon.BillingSettler, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &SubscriptionFunding{
				requestId: relayInfo.RequestId,
				userId:    relayInfo.UserId,
				modelName: relayInfo.OriginModelName,
				amount:    subConsume,
			},
		}
		// 必须传 subConsume 而非 preConsumedQuota，保证 SubscriptionFunding.amount、
		// preConsume 参数和 FinalPreConsumedQuota 三者一致，避免订阅多扣费。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch pref {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, err := tryWallet()
		if err != nil {
			if err.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
		if subCheckErr != nil {
			return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSub {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				// 仅当用户的活跃订阅允许钱包回退时才回退到钱包，否则返回订阅额度不足错误
				allowOverflow, overflowErr := model.UserActiveSubscriptionsAllowWalletOverflow(relayInfo.UserId)
				if overflowErr != nil {
					return nil, types.NewError(overflowErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
				}
				if allowOverflow {
					return tryWallet()
				}
				return nil, apiErr
			}
			return nil, apiErr
		}
		return session, nil
	}
}

func (s *BillingSession) finishLegacyLease(state string) error {
	if s.legacyLeaseID == "" {
		return nil
	}
	return model.FinishProductLegacyFundingLease(context.Background(), s.legacyLeaseID, state)
}

// The native key journal is independent of the original funding engine. Every
// funding call first records UNKNOWN; a crash/result uncertainty blocks automatic
// replay, while a completed engine call can retry only its same token receipt.
func (s *BillingSession) usesNativeTokenQuota() bool {
	return s.relayInfo != nil && s.relayInfo.NativeRealm && !s.relayInfo.IsPlayground && s.relayInfo.TokenId > 0
}

func (s *BillingSession) nativeTokenSessionID() string {
	if s.nativeQuotaSessionID == "" {
		if s.relayInfo.RequestId == "" {
			s.relayInfo.RequestId = common.GetUUID()
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s", s.relayInfo.UserId, s.relayInfo.TokenId, s.relayInfo.RequestId, s.funding.Source())))
		s.nativeQuotaSessionID = fmt.Sprintf("native-quota:%x", digest)
	}
	return s.nativeQuotaSessionID
}

func nativeFundingDeclined(err error) bool {
	return errors.Is(err, ErrInsufficientWalletQuota) || strings.Contains(err.Error(), "no active subscription") || strings.Contains(err.Error(), "subscription quota insufficient")
}

func (s *BillingSession) beginNativeFunding(stage string, amount int) error {
	if err := model.BeginNativeTokenFundingStage(s.relayInfo.TokenId, s.nativeTokenSessionID(), stage, amount); err != nil {
		s.reconciliationPending = true
		return err
	}
	return nil
}

func (s *BillingSession) finishNativeFunding(stage string, fundingErr error) error {
	outcome := "confirmed"
	if fundingErr != nil {
		if !nativeFundingDeclined(fundingErr) {
			s.reconciliationPending = true
			return fundingErr
		}
		outcome = "declined"
	}
	if err := model.FinishNativeTokenFundingStage(s.relayInfo.TokenId, s.nativeTokenSessionID(), stage, outcome); err != nil {
		s.reconciliationPending = true
		return err
	}
	return fundingErr
}

func (s *BillingSession) preConsumeNative(c *gin.Context, quota int) *types.NewAPIError {
	if quota < 0 {
		return types.NewError(model.ErrNativeQuotaConflict, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	effective := quota
	if s.shouldTrust(c) {
		s.trusted = true
		effective = 0
	}
	accepted, err := model.ReserveNativeTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.nativeTokenSessionID(), effective)
	if err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	if !accepted {
		return types.NewErrorWithStatusCode(ErrInsufficientTokenQuota, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	s.tokenConsumed = effective
	if err = s.beginNativeFunding("preconsume", effective); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	fundingErr := s.funding.PreConsume(effective)
	err = s.finishNativeFunding("preconsume", fundingErr)
	if err != nil {
		if s.reconciliationPending {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		if rollbackErr := model.RefundNativeTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.nativeTokenSessionID()); rollbackErr != nil {
			return types.NewError(rollbackErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		s.tokenConsumed = 0
		if errors.Is(fundingErr, ErrInsufficientWalletQuota) {
			return insufficientWalletQuotaError(s.relayInfo.UserId)
		}
		return types.NewErrorWithStatusCode(fundingErr, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	s.preConsumedQuota = effective
	s.syncRelayInfo()
	return nil
}

func (s *BillingSession) settleNativeLocked(actualQuota int) error {
	if s.nativeSettlementStarted && s.nativeSettlementQuota != actualQuota {
		return model.ErrNativeQuotaConflict
	}
	if actualQuota < 0 {
		return model.ErrNativeQuotaConflict
	}
	if s.reconciliationPending {
		return model.ErrNativeFundingReconciliation
	}
	if s.settled {
		return nil
	}
	if s.refunded || s.nativeFundingRefunded {
		return model.ErrNativeQuotaConflict
	}
	delta := actualQuota - s.preConsumedQuota
	s.nativeSettlementQuota, s.nativeSettlementStarted = actualQuota, true
	if !s.fundingSettled {
		stage := fmt.Sprintf("settle:%d", actualQuota)
		if err := s.beginNativeFunding(stage, delta); err != nil {
			return err
		}
		if err := s.finishNativeFunding(stage, s.funding.Settle(delta)); err != nil {
			return err
		}
		s.fundingSettled = true
	}
	if err := model.SettleNativeTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.nativeTokenSessionID(), actualQuota); err != nil {
		return err
	}
	if s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.settled = true
	return s.finishLegacyLease("settled")
}

func (s *BillingSession) reserveNativeLocked(targetQuota int) error {
	if s.reconciliationPending {
		return model.ErrNativeFundingReconciliation
	}
	if s.nativeFundingRefunded || s.fundingSettled {
		return model.ErrNativeQuotaConflict
	}
	if s.settled || s.refunded || targetQuota <= s.preConsumedQuota {
		return nil
	}
	delta := targetQuota - s.preConsumedQuota
	stage := fmt.Sprintf("reserve:%d", targetQuota)
	if err := s.beginNativeFunding(stage, delta); err != nil {
		return err
	}
	if err := s.finishNativeFunding(stage, s.reserveFunding(delta)); err != nil {
		return err
	}
	accepted, err := model.ReserveNativeTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.nativeTokenSessionID(), targetQuota)
	if err != nil && errors.Is(err, model.ErrNativeQuotaUnknown) {
		s.reconciliationPending = true
		return err
	}
	if err != nil || !accepted {
		rollbackStage := fmt.Sprintf("reserve-rollback:%d", targetQuota)
		if rollbackErr := s.beginNativeFunding(rollbackStage, -delta); rollbackErr != nil {
			return rollbackErr
		}
		if rollbackErr := s.finishNativeFunding(rollbackStage, s.rollbackFundingReserve(delta)); rollbackErr != nil {
			return rollbackErr
		}
		if err != nil {
			return err
		}
		return ErrInsufficientTokenQuota
	}
	s.preConsumedQuota, s.tokenConsumed = targetQuota, targetQuota
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

func (s *BillingSession) refundNative(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || s.fundingSettled || s.reconciliationPending {
		return
	}
	if !s.nativeFundingRefunded {
		if err := s.beginNativeFunding("refund", -s.tokenConsumed); err != nil {
			common.SysError(err.Error())
			return
		}
		if err := s.funding.Refund(); err != nil {
			s.reconciliationPending = true
			common.SysError("native funding refund requires reconciliation: " + err.Error())
			return
		}
		if s.extraReserved > 0 && s.funding.Source() == BillingSourceSubscription && s.relayInfo.SubscriptionId > 0 {
			if err := model.PostConsumeUserSubscriptionDelta(s.relayInfo.SubscriptionId, -int64(s.extraReserved)); err != nil {
				s.reconciliationPending = true
				common.SysError("native subscription extra refund requires reconciliation: " + err.Error())
				return
			}
		}
		if err := s.finishNativeFunding("refund", nil); err != nil {
			common.SysError(err.Error())
			return
		}
		s.nativeFundingRefunded = true
	}
	if err := model.RefundNativeTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.nativeTokenSessionID()); err != nil {
		common.SysError("native token refund receipt pending: " + err.Error())
		return
	}
	s.refunded = true
	if err := s.finishLegacyLease("refunded"); err != nil {
		common.SysError(err.Error())
	}
}
