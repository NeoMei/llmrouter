package common

import "github.com/gin-gonic/gin"

// BillingSettler 抽象计费会话的生命周期操作。
// 由 service.BillingSession 实现，存储在 RelayInfo 上以避免循环引用。
type BillingSettler interface {
	// Settle 根据实际消耗额度进行结算，计算 delta = actualQuota - preConsumedQuota，
	// 同时调整资金来源（钱包/订阅）和令牌额度。
	Settle(actualQuota int) error

	// Refund 退还所有预扣费额度（资金来源 + 令牌），幂等安全。
	// 通过 gopool 异步执行。如果已经结算或退款则不做任何操作。
	Refund(c *gin.Context)

	// NeedsRefund 返回会话是否存在需要退还的预扣状态（未结算且未退款）。
	NeedsRefund() bool

	// GetPreConsumedQuota 返回实际预扣的额度值（信任用户可能为 0）。
	GetPreConsumedQuota() int

	// Reserve 将预扣额度补到目标值；若目标值不高于当前预扣额度则不做任何事。
	Reserve(targetQuota int) error

	// MarkReconciliationPending 把预扣状态标记为「结果未确认，需要人工/自动对账」。
	// 标记后既不按估算 usage 结算，也不允许 Refund 静默退还预扣费；资金保持预扣，
	// 等待基于供应商真实用量与账本的对账结果处理。
	MarkReconciliationPending(reason string)

	// IsSettled 返回本次请求的预扣费是否已经成功结算。失败路径据此判断
	// 供应商用量是否真的落到本地账本，避免把结算失败写成已扣费。
	IsSettled() bool
}
