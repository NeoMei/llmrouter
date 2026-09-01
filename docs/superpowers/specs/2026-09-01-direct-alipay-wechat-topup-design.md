# 支付宝与微信官方直连充值设计

## 目标

让钱包充值页现有的“支付宝”和“微信”付款方式直接连接商户自己的官方支付渠道：

- “支付宝”使用已经申请的支付宝电脑网站支付 `alipay.trade.page.pay`。
- “微信”使用已经实现的微信支付 APIv3 Native 支付。
- 两个入口继续出现在现有付款方式区域，不再新增一张独立的人民币充值卡。
- 支付宝、微信的回调都只结算本地 `TopUp` 订单，并且同一笔官方交易最多增加一次钱包额度。
- Stripe、Waffo、Waffo Pancake 保持独立，既不承载支付宝/微信直连，也不改变现有结算语义。

“直连”指请求直接发送到支付宝开放平台或微信支付 API，款项由对应官方商户渠道结算。实现可以使用经过固定版本和安全审查的 SDK，但不经过 Epay 或其他聚合收单网关。

本设计取代 `2026-08-31-native-wechat-pay-topup-design.md` 中“支付宝不在首期范围”“微信使用独立 CNY 卡片”和“支付宝未来再加入 CNY 区域”的界面/范围决定。旧文档中的微信 APIv3 验签、精确金额、幂等结算、后台对账和回滚安全约束继续有效；若两份文档冲突，以本设计为准。

## 当前问题与根因

生产环境当前的 `PayMethods` 配置包含三个静态展示项：

- `alipay` / 支付宝 / 最低 50
- `wxpay` / 微信 / 最低 50
- `custom1` / 自定义1 / 最低 50

这些项属于旧 Epay 协议。前端点击后统一调用 `POST /api/user/pay`，后端再向 `<PayAddress>/submit.php` 提交 Epay 的 `pid` 和 MD5 签名。生产环境没有可用的 Epay 网关凭证，因此这些卡片虽然显示，却不是可用的官方支付入口。

此前实现的微信官方直连走独立的 `CnyTopupCard`、`POST /api/user/wechat-pay/native` 和二维码弹窗。生产镜像已经包含该实现，但 `WECHATPAY_ENABLED=false`，所以独立卡片也不会显示。这个实现没有修改 Waffo Pancake，却没有接管用户指出的原“微信”按钮。

本设计修正的是入口与支付提供方的绑定关系，而不是再增加一个并行入口。

## 已确认范围

本阶段包含：

- 一次性钱包充值。
- 支付宝电脑网站支付下单、跳转、异步通知验签、订单查询、超时关单和对账。
- 微信 Native 下单、二维码、异步通知、订单查询、超时关单和对账。
- 原支付宝/微信方法卡的直连路由。
- 两个提供方共用的精确金额快照、幂等结算和异常审计规则。
- 保留现有 Stripe、Waffo、Waffo Pancake 和兑换码能力。

本阶段不包含：

- Epay 网关申请、自建 Epay 转换服务或 Epay 协议扩展。
- 支付宝当面付、手机网站支付、App 支付。
- 微信 JSAPI、小程序、App 或 H5 支付。
- 订阅购买、自动续费、自动退款或用户自助退款。
- 独立的多币种储值钱包。

## 入口与支付方式契约

### 方法描述

`GET /api/user/topup/info` 返回的每个付款方式增加两个明确字段：

- `provider`：实际处理订单的提供方，例如 `alipay`、`wechat_pay`、`epay`、`stripe`。
- `flow`：前端动作，例如 `redirect`、`qr` 或 `form_post`。

稳定的直连方法描述为：

```json
{
  "name": "支付宝",
  "type": "alipay",
  "provider": "alipay",
  "flow": "redirect",
  "currency": "CNY",
  "min_topup": "1.00"
}
```

```json
{
  "name": "微信",
  "type": "wxpay",
  "provider": "wechat_pay",
  "flow": "qr",
  "currency": "CNY",
  "min_topup": "1.00"
}
```

`type` 继续决定用户看到的名称和图标，`provider` 才决定调用哪个后端。前端不得再把所有非 Stripe/Waffo 方法默认视为 Epay。

### 可用性

后端只返回真正可下单的方法：

- 支付宝配置完整且启用时，返回官方直连支付宝。
- 微信配置完整且启用时，返回官方直连微信。
- Epay 的网关地址、PID、KEY 和回调条件全部满足时，才返回 Epay 方法。
- Epay 未启用时，不返回静态 `PayMethods`，因此不再显示灰色的“支付宝/微信/自定义1”假入口。
- 若直连与 Epay 都声明 `alipay` 或 `wxpay`，官方直连优先，抑制同名 Epay 项，避免两个同名按钮。
- `custom1` 仅在真实 Epay 网关启用且明确配置时显示；当前生产环境隐藏。

`enable_online_topup` 不再单独代表 Epay。付款区域是否可用以规范化后的方法列表为准，旧布尔字段仅保留兼容期。

## 钱包界面

移除独立的 `CnyTopupCard`，把其精确人民币输入能力合并到现有充值表单：

1. 用户输入人民币充值金额，前端保留十进制定价文本，不先转换为 JavaScript 浮点金额。
2. 付款方式区域继续显示支付宝、微信、Stripe、Waffo 等方法卡。
3. 点击支付宝后显示一次确认，再跳转支付宝官方电脑网站收银台。
4. 点击微信后显示一次确认，再打开现有微信二维码弹窗。
5. 支付成功后刷新钱包余额和订单记录。

人民币直连的输入金额、官方实付金额和该订单购买的人民币等值额度保持一致。例如输入 `50`，支付宝或微信订单金额都是 `¥50.00`。后端按订单创建时的美元汇率和 `QuotaPerUnit` 计算并保存 `credited_quota`。汇率后来变化不能改变已创建订单的到账额度。

Stripe、Waffo 等国际支付继续使用其现有报价和结账逻辑。选择付款方式后，界面的“待支付金额”必须显示该方法的真实币种和报价，不能把人民币金额标签错误复用于美元订单。

付款方法最低金额来自实际提供方配置，不再使用当前静态 `PayMethods` 中的 `50`。直连默认建议范围是 `¥1.00` 至 `¥5000.00`，生产值可通过各提供方环境变量单独调整。

## 共享订单与结算边界

继续使用 `TopUp` 作为唯一订单和权益记录。现有字段已经能够支持两个直连提供方：

- `payment_provider`
- `payment_method`
- `currency`
- `paid_amount_minor`
- `credited_quota`
- `provider_trade_no`
- `next_reconcile_time`
- `reconcile_attempts`
- `status`

新增支付宝常量 `PaymentProviderAlipay = "alipay"` 和 `PaymentMethodAlipay = "alipay"`。微信历史订单继续存储 `PaymentProviderWeChatPay = "wechat_pay"` 与 `PaymentMethodWeChatNative = "wechat_native"`；前端方法类型 `wxpay` 只是一层稳定入口映射，不重写历史订单。

把现有微信专用结算核心收敛为提供方中立的精确充值结算操作，并保留微信、支付宝的薄包装：

1. 使用 `lockForUpdate(tx)` 锁定本地订单。
2. 校验期望的 `payment_provider`、`payment_method`、`currency` 和 `paid_amount_minor`。
3. 要求订单为 `pending`，或已是同一笔交易结算成功的幂等重复。
4. 要求官方交易号为空或等于当前已验证交易号。
5. 借助现有复合唯一索引，禁止一个官方交易号结算两张本地订单。
6. 在同一事务中增加保存的 `credited_quota`、记录官方交易号、完成时间和 `success` 状态。
7. 事务提交后再同步额度缓存和写入充值审计日志。

提供方回调和后台对账必须调用同一个结算操作。浏览器回跳、前端轮询以及人工刷新都不是到账依据。

## 微信支付直连

保留现有 `service/wechat_pay.go`、APIv3 SDK、回调验证、二维码弹窗和后台对账，仅调整入口：

- `wxpay + wechat_pay + qr` 调用现有 `POST /api/user/wechat-pay/native`。
- `GET /api/user/wechat-pay/orders/:trade_no` 继续只读取本地订单状态。
- `POST /api/wechat-pay/notify` 继续负责签名验证、资源解密和结算。
- 现有独立 `CnyTopupCard` 删除，二维码对话框保留。

微信商户号和 AppID 已知，商户 API 证书及私钥包已经校验匹配。正式启用仍要求 APIv3 密钥文件、微信支付公钥 ID 和与该 ID 对应的微信支付公钥文件齐全。商户 API 证书序列号不能替代微信支付公钥 ID。

## 支付宝电脑网站支付直连

### Provider 服务

新增 `service/alipay_pay.go`，通过小型 `AlipayPayProvider` 接口隔离 SDK：

- `CreatePagePay`：生成 `alipay.trade.page.pay` 的官方 HTTPS 跳转 URL。
- `QueryOrder`：调用 `alipay.trade.query`。
- `CloseOrder`：调用 `alipay.trade.close`。
- `VerifyNotify`：验证支付宝异步通知的 RSA2 签名并规范化交易字段。

实现采用固定版本的 `github.com/smartwalle/alipay/v3`，并在实施时完成许可证、传递依赖和维护状态审查。该库只负责本地签名、验签和直接调用支付宝开放平台，不引入托管收银网关。所有业务状态在进入 controller/model 前转换为项目自己的结构，避免 SDK 类型渗透到结算层。

不手写 RSA2 拼接、签名或验签算法。

### API 路由

新增：

- `POST /api/user/alipay/page`：创建或恢复当前用户的电脑网站支付订单并返回官方跳转 URL。
- `GET /api/user/alipay/orders/:trade_no`：读取当前用户的本地订单状态。
- `POST /api/alipay/notify`：接收支付宝服务器异步通知。
- `GET /api/alipay/return`：验证同步回跳参数后跳回钱包/订单记录；不执行到账。

支付宝下单请求使用十进制人民币字符串和客户端幂等键。商户订单号使用 `ALP` 加用户 ID 与幂等键摘要，满足支付宝长度要求。同一幂等键只能对应同一用户、金额、币种和额度快照。

本地 `pending` 订单先创建，再生成带以下固定业务参数的官方跳转 URL：

- `product_code = FAST_INSTANT_TRADE_PAY`
- 本地唯一 `out_trade_no`
- 精确两位人民币 `total_amount`
- 不暴露内部额度数据的真实商品标题
- `timeout_express = 15m`
- 可信 HTTPS `notify_url`
- 可信 HTTPS `return_url`

后端只返回 `https://openapi.alipay.com/` 下的已签名跳转 URL；沙箱模式只允许支付宝官方沙箱网关。前端在跳转前再次校验协议和精确主机，不接收任意第三方 URL 或后端返回的可执行 HTML。

### 异步通知

支付宝异步通知是唯一实时到账依据。处理顺序：

1. 以 `application/x-www-form-urlencoded` 读取完整参数。
2. 在读取任何业务字段前验证 RSA2 签名。
3. 要求通知 `app_id`、配置的 `seller_id`、商户订单号和本地订单提供方全部匹配。
4. 只接受 `TRADE_SUCCESS` 或 `TRADE_FINISHED`。
5. 把 `total_amount` 以十进制转换为分，并与 `paid_amount_minor` 精确比较。
6. 要求币种语义为 CNY，并验证支付宝 `trade_no` 未结算其他订单。
7. 调用共享结算操作。

有效且成功或幂等重复的通知返回纯文本 `success`。签名失败、解析失败或临时数据库错误返回 `failure`，让支付宝重试。签名有效但订单未知、身份不符、金额冲突或交易号冲突时，先持久化管理员可见的支付异常记录，再返回 `success`，避免无法自愈的通知重试风暴；异常记录失败则返回 `failure`。

同步 `return_url` 仅改善浏览器体验。即使参数验签通过，也只展示“结果确认中”并读取本地订单状态，绝不直接增加额度。

## 查询、关单与对账

微信和支付宝各自保留独立 provider 查询器，但共享本地认领、退避和结算规则：

- 主节点定时扫描各提供方到期的 `pending` 订单。
- 条件更新 `next_reconcile_time` 和 `reconcile_attempts` 后才查询官方状态，避免多实例放大流量。
- 官方成功状态必须再次校验商户身份、订单号、金额、币种和交易号，再调用共享结算。
- 未支付状态保持 `pending`。
- 超过 15 分钟后先查询，再尝试官方关单；只有官方确认关闭后本地才变为 `expired`。
- 网络超时、5xx 或不明确结果保持 `pending`，等待下一轮。

T+1 运营对账必须分别下载支付宝和微信交易账单，与本地 `success` 充值记录核对。官方成功而本地未到账、金额不一致或同一交易重复关联都进入人工处理记录。部署回滚不能撤销已经成功到账的财务记录。

## 配置与密钥

### 支付宝

- `ALIPAY_ENABLED`
- `ALIPAY_APP_ID`
- `ALIPAY_SELLER_ID`
- `ALIPAY_APP_PRIVATE_KEY_PATH`
- 公钥模式：`ALIPAY_PUBLIC_KEY_PATH`
- 证书模式：`ALIPAY_APP_CERT_PATH`、`ALIPAY_PUBLIC_CERT_PATH`、`ALIPAY_ROOT_CERT_PATH`
- `ALIPAY_MIN_TOPUP_CNY`
- `ALIPAY_MAX_TOPUP_CNY`
- `ALIPAY_SUBJECT`
- 可选 `ALIPAY_SANDBOX`，生产默认关闭

第一版 provider 同时支持支付宝公钥模式和证书模式，启动时必须且只能选择一套完整配置，不得混用或缺半套。两种模式都执行独立的配置就绪和验签测试；生产部署使用用户支付宝应用当前启用的模式，切换模式不改变订单和结算层。

### 微信

继续使用现有 `WECHATPAY_*` 文件路径和标识配置。APIv3 密钥、支付宝应用私钥、微信商户私钥只存放在权限为 `0600` 的只读挂载文件中。

所有回调 URL 从可信的 `ServerAddress` 派生。私钥、APIv3 密钥、完整签名、Authorization 头和完整回调正文不得进入数据库设置、浏览器响应、镜像层或日志。

## 错误处理与可观察性

- 配置未就绪时，后端不返回对应付款方式，并拒绝直接调用下单接口。
- 创建订单的明确参数/权限失败可标记 `failed`；网络和提供方不明确结果保持 `pending`。
- 记录本地订单号、提供方请求 ID/trace ID、官方交易号、状态转换和高层错误码。
- 不记录签名原文、私钥、APIv3 密钥或完整解密/回调载荷。
- 身份、金额、币种、交易号冲突必须形成管理员可见异常记录。
- 前端失败消息区分“通道未配置”“订单创建失败”“支付结果确认中”和“订单已关闭”，但不暴露提供方内部响应。

## 测试策略

所有行为修改按红—绿—重构的 TDD 顺序完成。

### 后端

- 直连方法只在对应配置完整时出现在 `topup/info`。
- Epay 未配置时不再泄漏静态支付宝、微信、自定义1卡片。
- `alipay` 和 `wxpay` 分别路由到官方直连 controller，不调用 Epay。
- 人民币字符串到分、额度快照、最小/最大金额和 int32 上限测试。
- 支付宝跳转 URL 只允许官方网关，且参数包含正确订单号、金额、通知和返回地址。
- 支付宝回调在签名验证前不读取可信业务状态。
- AppID、seller ID、订单号、状态、金额或交易号冲突不能到账。
- 支付宝、微信成功回调和查询竞态最多增加一次额度。
- 同一官方交易号不能结算两张订单。
- 同步回跳永远不能结算。
- 查询、关单、退避、重启和重复调度保持正确状态机。
- SQLite、MySQL、PostgreSQL 继续使用相同 GORM 迁移和锁定约束。

### 前端

- 原付款方式区域显示可用的官方支付宝和微信按钮。
- Epay 未配置时不显示自定义1或旧 Epay 假入口。
- 点击支付宝调用直连支付宝 API 并只跳转官方 HTTPS 主机。
- 点击微信调用直连微信 API 并打开二维码弹窗。
- 独立 `CnyTopupCard` 不再渲染。
- 人民币十进制文本不会在请求前经过浮点重算。
- 方法最低金额、真实支付币种、加载状态和失败提示准确。
- Stripe、Waffo、Waffo Pancake 的既有分发测试继续通过。

### 真实验收

1. 支付宝先在官方沙箱完成创建、跳转、通知验签和幂等到账测试。
2. 两个通道以禁用状态部署，确认旧支付回归和方法可用性门控。
3. 补齐生产密钥并检查只读权限，不输出秘密值。
4. 逐个开启通道，每次只验证一个提供方。
5. 在用户付款当刻再次取得明确确认，再进行一笔最小真实支付。
6. 验证本地 `pending -> success`、精确金额、一次额度增加、缓存同步和订单记录。
7. 重放通知或重复查询，余额不得再次增加。
8. 留一笔未支付订单，确认官方关单后才标记 `expired`。
9. 保存首次账单对账结果。

## 发布与回滚

- 数据库和代码先以 `ALIPAY_ENABLED=false`、`WECHATPAY_ENABLED=false` 发布。
- 新版本上线后，原支付宝/微信假入口应消失，Stripe/Waffo 等已配置通道保持可用。
- 每个提供方只有在全部凭证和回调检查通过后才显示。
- 可分别关闭支付宝或微信的新订单，不影响另一通道。
- 关闭新订单后仍保留凭证和后台对账，直到已有 `pending` 订单终结。
- 镜像回滚保留订单表、支付异常记录和已经到账的额度，不执行财务数据逆向迁移。

## 成功标准

- 用户在截图所示原付款方式区域点击“支付宝”或“微信”，实际进入对应官方商户支付流程。
- 代码、接口响应和浏览器都不再把这两个按钮发送到 Epay 或 Waffo。
- 支付成功只能由验签回调或可信官方查询结算，且到账一次。
- 通道未配置时不展示误导性按钮。
- Stripe、Waffo 和其他现有通道无行为回归。
