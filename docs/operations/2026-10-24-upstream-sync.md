# 2026-10-24 上游选择性同步记录

本次按行为移植 New API 和 OpenCodex 的兼容性、计费与运行稳定性修复。LLMRouter 与上游已有较大架构差异，完整合并会产生 214 个冲突文件；因此采用独立分组提交、针对性回归、独立审查后本地快进合并的方式。

原始基线为 `bb7ce224f60dd16092c32d6be4cea14828f0ca86`，原始分支为 `codex/wechat-native-pay`；实施分支为 `codex/upstream-sync-20261024`。本次核对的 New API main 快照为 `33c61a24234aa20cb10e8a644304322c476361de`，rc.46 基线为 `7d8b91268dab69d7150f345c448b5e250163146a`。这是一组已选修复的同步，不表示 LLMRouter 的所有功能已与上游版本完全对齐。

## 已同步内容

| 范围 | 行为变化 | 来源 |
| --- | --- | --- |
| Go 工具链 | 模块、全部 Go 工作流和 Docker 的服务端、onboarding 构建阶段统一到 Go 1.26.10 | New API [7d8b91268](https://github.com/QuantumNous/new-api/commit/7d8b91268dab69d7150f345c448b5e250163146a)，rc.46 |
| 上游响应头超时 | 限制等待响应头的时间；已开始的流式正文不受该计时器限制；正数溢出安全处理 | New API [b518d0033](https://github.com/QuantumNous/new-api/commit/b518d0033b670f5518b8a2f1cf8ea0142a9d1b8d)，rc.46 |
| Redis 限流 | SCRIPT FLUSH 后从 NOSCRIPT 恢复，同时保留已有令牌桶状态 | New API [e2a0c4b16](https://github.com/QuantumNous/new-api/commit/e2a0c4b16aa6c2e365f0845cf073c52b933385a2)，rc.46 |
| 访问日志 | 查询凭据脱敏，OAuth 回调查询整体省略；请求本身保持原值 | New API [30748c655](https://github.com/QuantumNous/new-api/commit/30748c6558bc97de49ff74871cfec717e50f3322)，rc.46 之后 |
| 上游重试指示 | `X-Should-Retry: false` 进入现有禁止重试策略；`true` 不覆盖本地限制 | OpenCodex [30e82f9a](https://github.com/lidge-jun/opencodex/commit/30e82f9a287020f47797db54b08012ef80363f27) |
| Claude usage | 保留合法零用量，修正只有缓存用量时的计费与统计，并在估算后更新统一用量数据 | New API [1b63c28dc](https://github.com/QuantumNous/new-api/commit/1b63c28dcc999c4445c70f21ec90103ab49e8be5)，rc.46 |
| 缓存写入表达式计费 | 未标明 5 分钟/1 小时分项的缓存写入余量按默认 5 分钟计入 | New API [94cc3ff09](https://github.com/QuantumNous/new-api/commit/94cc3ff0906db37703d5ce2bc6a479209b7b9b23)，rc.46 之后 |
| 任务重试预扣 | 每次按当前渠道/分组价格，在发送前补足已有预扣；余额不足时终止，不误记或停用健康渠道 | New API [55e2ae54d](https://github.com/QuantumNous/new-api/commit/55e2ae54d1445d92482ac9bb27b2409bb8f46b70)，rc.46；另补本地错误策略传递 |
| Responses 完成判定 | 最终事件实际写入和刷新成功才计成功；失败不保存连续会话；合法终止事件后干净 EOF 可完成 | New API [0c2af9780](https://github.com/QuantumNous/new-api/commit/0c2af9780e45f6f9073528fb0fcbdcd64b5d9495)，rc.46 之后 |
| 大事件与慢客户端 | 流式写入期限根据累计字节数计算，保留已有字节的排空时间 | New API [7e6596728](https://github.com/QuantumNous/new-api/commit/7e6596728ff640392bd77169ebb6137a8fe0c309)，rc.46 之后 |
| Claude 流式保活 | 请求准备与跨重试期间持续保活；正式输出前停止并等待保活退出；已发送 SSE 头后以 SSE 返回错误 | New API [25372e30b](https://github.com/QuantumNous/new-api/commit/25372e30b1f94255b1f818aeaaf006f042572580)，rc.46 |
| 相邻 assistant 合并 | 保留所有工具调用、调用 ID、对应工具结果及正文 | New API [783735607](https://github.com/QuantumNous/new-api/commit/783735607d863bc182a00b939e498cc22e92f2f2)，rc.46 |
| Codex 控制项 | 翻译协议时省略 `configuration_update`，避免生成空用户消息；明确支持的原生目的地保留 | OpenCodex [561347cc](https://github.com/lidge-jun/opencodex/commit/561347cca9cb885ec479009593dee20747dae3c2) |
| Claude thinking 往返 | Chat 的 `reasoning_details` 保留签名和 redacted thinking；流式状态按请求、内容块隔离 | OpenCodex [7a9dc77b](https://github.com/lidge-jun/opencodex/commit/7a9dc77bdbb89b1e15dd6cac869a4b87a1c91a0b) |

## 本地适配和配置

- 新配置 `RELAY_RESPONSE_HEADER_TIMEOUT` 的默认值是 `1800` 秒，设为 `0` 可关闭。它只控制响应头等待，原有整次请求超时设置仍然有效。直接连接与代理连接使用同一传输构造逻辑。
- 保留 Gateway 渠道编号 **59**、原子钱包预扣/退款、最终分组计费、配额饱和保护和审计、BYOK、onboarding、支付流程及现有渠道编号。
- 保留 Responses 严格工具校验和会话连续性的作用域限制。不会仅收到 `response.completed` 就立即停止读取：仍需 `[DONE]` 或干净 EOF、校验通过和最终输出成功。上游发送 completed 后一直不关闭且不发 `[DONE]` 的情况仍由流超时限制。
- HTTP 200 下发送的 SSE 应用错误不会增加“成功请求”限流计数。快速失败仍按原有 HTTP/JSON 路径返回。
- Claude 签名只向匹配的 Anthropic 官方目的地或明确支持该载体的 OpenRouter 保留；其他协议转换保留普通 reasoning 与未知厂商元数据。显式原始请求透传继续保持原始正文。
- 已知 OpenRouter 原生 Responses 保留 `configuration_update`，依据核对时的[官方 reasoning 文档](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens)。目的地匹配采用渠道身份和精确 HTTPS 主机名，不使用宽泛子串匹配。
- 没有引入数据库迁移或新增方言 SQL。任务插件、RelayKit 架构迁移、scoped access tokens、新渠道能力和上游 GUI 改版留待单独评估。

## 验证记录

最终代码提交：`0b5b71e16`。以下检查均在该代码上通过；后续仅补充文档。

| 检查 | 结果 |
| --- | --- |
| `GOTOOLCHAIN=go1.26.10 bash scripts/verify-backend.sh` | 退出码 0：gofmt、全量 `go test ./...`、`go vet ./...`、全量 `go test -race ./...`、`go build -trimpath ./...`、onboarding 单独回归通过 |
| `bun install --frozen-lockfile`、`bun run build`、`bun run typecheck`（`web/`） | 通过；后端验证嵌入真实构建的前端资源 |
| `python3 -m unittest scripts/release_ci_contract_test.py -v` | 7 项通过 |
| `bash scripts/release_config_test.sh` | 通过 |
| `bash scripts/release_scripts_test.sh` | 通过 |
| 固定 digest 的 Go Docker 镜像运行 `go version` | `go1.26.10 linux/arm64`，通过 |
| 分组及全分支独立审查 | 全部通过；任务计费错误策略的 P2 已修复并复审通过；最终全分支审查无新增可操作问题 |

新增回归覆盖了真实本地 HTTP 转发、Redis 协议交互、SQLite 计费余额变化、最终事件写入失败、保活交接、工具调用配对和签名转换。审查发现的任务计费错误分类丢失已修复，控制器回归确认余额不足时不发请求、不重试、不写渠道错误日志且不停用健康渠道。异步退款测试清理阶段的竞态也已修复，全量 race 检查通过。

验证边界：没有执行生产部署、真实支付、真实上游签名认证或慢公网吞吐验收；数据库执行验证使用 SQLite，MySQL/PostgreSQL 未另起环境运行。全量后端测试包含已有支付相关回归，但不等于真实支付验收。

本地证据保存在原项目的 `.superpowers/sdd/2026-10-24-upstream-sync/`（Git 忽略目录），包括分组实施、失败复现、独立审查以及 `verification/llmrouter-sync-final-backend.log` 等验证日志。证据目录在清理实施 worktree 前保留到原项目；本文件提供可随版本库保存的摘要。

## 提交拆分

- `2f6599822`：同步范围与验证计划。
- `f4d4da16f`：工具链、传输超时、日志、限流与重试指示。
- `2b5cc2411`：Claude 缓存计费与任务重试预扣。
- `379217f4c`：流式完成、保活与写入期限。
- `504a56b25`：Codex 控制项与 Claude thinking 往返。
- `0b5b71e16`：审查修复，保留本地任务计费错误策略并加强响应正文超时回归。

本次仅进行本地同步；远端发布和运行环境升级不在本次交付范围内。
