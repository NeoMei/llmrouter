# 国产模型账号池：2026-10-25 本地验证记录

本文对应[接口与运维指南](domestic-model-account-pools.md)，记录本地源码验收，不是生产发布证明。分项审查及最终本地后端/前端/发布契约门禁已通过。首轮发现的四操作契约清单遗漏已修复并独立审查，失败原始证据保留。全分支独立审查、本地合入和证据归档仍由交付负责人执行。

## 源码与证据身份

首轮全量检查 HEAD 为 `3dada17c87dbe52eb4ea8a3089a6efd642946a32`，Git tree 为 `b9bf687651b61f761416f6d1b73f130e911157eb`，开始时工作区干净。相对最后生产代码提交 `1a70e2dc1` 只有文档提交。前端产物来自该生产代码的最终 `bun run build`（2.35 秒），231 个产物文件的逐文件 SHA256 保存在 `source-before.json`；没有用空白占位目录满足 Go embed。最终完整后端验证对应 `6a2db5a395278b9d2b8c15b444acc99e8264e3e8`（tree `8c38555b2a23d04ac75e455605f2967b68a26f2e`），相对首轮只追加 `tests/contracts/provider_fixtures.json` 中遗漏的四操作；没有生产代码变更。`source-final-before.json` 和 `source-after.json` 证明验证前后 HEAD 相同、231 个资产摘要完全一致，未提交差异只有本文与运维指南。

下面的 `W` 指工作树中的 `.superpowers/sdd/2026-10-24-domestic-multimodal-account-pools`。本轮证据在 `W/evidence/task-10c/`，每个命令分别保留 `.command.json`（cwd/argv/环境/开始时间/HEAD）、`.stdout.log`、`.stderr.log`、`.exit`、`.result.json`（结束时间/耗时）。后端另有 `.phases.log`，通过仅记录命令的 Bash DEBUG trap 留存阶段时间；没有测试过滤或套件排除。交付负责人须在清理工作树前归档整个 W（包括早期失败、审查 overlay、消费者二进制/来源、SQLite、浏览器 trace），并补充持久归档位置。不能只保留本文而删除原始日志。

环境为 darwin/arm64、Go1.26.10、CGO_ENABLED=1、Bun1.4.3。后端使用 `GOMAXPROCS=4 GOFLAGS=-p=4` 限制本机并行资源，不减少测试。两项 `LLMROUTER_ACCOUNT_POOL_MYSQL_DSN` / `LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN` 指向专用临时 MySQL5.7.44（65380）/PostgreSQL9.6（65381），完整夹具 DSN 仅在原始执行记录保存。未接触用户 `unihub-dev-*` 服务。

## 命令与结果

| 命令（注明 cwd） | 结果 | 原始证据 |
| --- | --- | --- |
| `bun run lint`（web） | PASS，136 个 warning，全部来自相对原交付基线 `50fd8d427` 未改动的文件；本分支改动文件 0 warning | `task-10c/web-lint.*`、`lint-warning-classification.json` |
| `bun run test`（web，默认完整发现） | PASS，124 测试、22 suites，0 失败/取消/跳过 | `task-10c/web-test.*` |
| `GOTOOLCHAIN=go1.26.10 bash scripts/verify-backend.sh`（根目录，另有上述并行/DSN 环境） | 最终 PASS，96.868 秒，全部六阶段通过；首轮 FAIL52.152 秒保留 | `task-10c/backend-final.*`、首轮 `backend.*` |
| `python3 -m unittest scripts/release_ci_contract_test.py -v`（根目录） | PASS，7 测试 | `task-10c/release-ci.*` |
| `bash scripts/release_config_test.sh`（根目录） | PASS，夹具配置与 `--check` 预检 | `task-10c/release-config.*` |
| `bash scripts/release_scripts_test.sh`（根目录） | PASS，包括 `restore_test.sh` 夹具完整性测试 | `task-10c/release-scripts.*` |

首轮唯一失败为 `tests/contracts/completeness_test.go:37` 的 `TestEverySupportedCapabilityHasProviderFixture`：能力注册表为 32 个操作，旧清单仅 28 个，遗漏 `multimodal.embeddings.create`、`ocr.jobs.create`、`ocr.jobs.retrieve`、`ocr.resources.retrieve`。实际对应 HTTP 夹具已存在，需补齐正确映射；不能通过删除能力、忽略测试或只声称 owner 测试通过来关闭全量门禁。首轮失败后未继续执行 vet、race、build、额外 onboarding。独立审查通过的清单修复后，以相同环境完整重跑原脚本，最终 exit0、stderr0 字节。没有重复已通过的前端/发布检查。普通 Go test 合法复用缓存；本轮 race 为实际执行，model66.739秒、router18.231秒、service/ocr5.253秒，没有race诊断。首次非缓存model44.454秒及既有逐项三数据库证据保留，不因缓存摘要重复已通过套件。

最终阶段时间（UTC，Bash阶段日志精度1秒；完整父进程起止为19:34:36.989–19:36:13.865）：

| 阶段 | 开始时间 | 完成依据 |
| --- | --- | --- |
| tracked Go gofmt | 19:34:37 | 无未格式化文件，进入test |
| `go test ./...` | 19:34:37 | 19:34:38进入vet |
| `go vet ./...` | 19:34:38 | 19:34:40进入race |
| `go test -race ./...` | 19:34:40 | 19:35:56进入build |
| `go build -trimpath ./...` | 19:35:56 | 19:36:11进入额外onboarding |
| `go test ./internal/onboarding/... ./cmd/llmrouter-onboard/... -count=1` | 19:36:11 | 三包PASS，父命令exit0 |

发布契约测试没有启动生产 Compose、读取生产配置或部署。`release_gate_test.sh` 与发布门禁实现相对此次基线未改动，本轮没有另跑它；早期已核对的 Docker Go1.26.10 验证也没有因状态汇报重复执行。

复用未改动代码的分项证据：

| 检查 | 最终结果与证据 |
| --- | --- |
| 前端 typecheck、单独浏览器 spec/config 类型检查 | PASS，`task-9b-fix-round-1/typecheck-final.*`、`fixture-typecheck-final.*` |
| 全前端格式、copyright、Knip | 格式 PASS1004 文件（`task-9b-fix-round-1/format-check.*`）；copyright PASS971 检查/1 generated skipped（`task-9b/copyright-check.*`）；Knip PASS（`task-9b/knip-green.*`） |
| 七语言 i18n | missing/extras/untranslated 均0；243 个所属代码键均有七语言条目（`task-9b/i18n-final.*`、`i18n-key-check.json`） |
| 所属单元测试 | PASS18/18（`task-9b-fix-round-1/units-final.*`），本轮默认124测试也包含这些用例 |
| Chrome 真实页面 | 全功能9/9 PASS25.2秒；最后 style-only 改动后相关3/3 PASS8.8秒（`task-9b-fix-round-1/browser*`），JSON结果和trace完整保留 |
| 前端生产构建 | PASS2.35秒（`task-9b-fix-round-1/build.*`）；在后端 embed 编译之前完成 |
| 实际固定消费者链 | 普通 PASS0.937秒、网关父进程 race PASS2.352秒（`task-10b/run-04`、`run-05-race`）；子消费者二进制未启用race |

普通全量后端明确不设置 `LLMROUTER_KNOWLEDGE_CONSUMER_BINARY`，因此其外部模块消费者测试按设计 opt-in 跳过；实际链已有上述独立证据。`TEST_MYSQL_DSN`、`TEST_POSTGRES_DSN` 未设置，旧 TokenMigration 和 user-session migration 外部数据库用例跳过；它们与本功能使用 `LLMROUTER_ACCOUNT_POOL_*` 的三数据库测试不同。`TEST_S3_ENDPOINT` 未设置，真实 S3 契约测试也不在本轮证明范围。默认 Go 包摘要不打印逐个 skip，环境与源码 skip 条件留存，不能将包 PASS 解读为不存在跳过。

## 操作与验收边界

“真实 HTTP”指临时网关/鉴权/账本加本地供应商 HTTP 服务；不是采购账号调用。“消费者”仅指固定版本实际后端客户端/检索器。“浏览器”指真实 Chrome 渲染与交互，API 响应由隔离夹具返回。

| 能力/操作 | 真实 HTTP 与计费/边界负责人 | 实际消费者 | SQL/浏览器证据 |
| --- | --- | --- | --- |
| 原生/Gateway 账号轮换、限流、共享组、BYOK、任务归属 | `TestRelayAccountPoolHTTP`、`TestRelayAccountPoolNativeCacheRefresh`、任务归属夹具；总发送预算、A429→B、禁止重试和一次计费 | OCR/文本向量链另证 A429→B，OCR 创建后 Key 重排仍 poll B | `TestUpstreamAccountConfiguredDatabases`：SQLite/MySQL5.7.44/PG9.6，两连接/实例、全局组和旧快照；管理浏览器见下行 |
| 管理读取/pause/resume/recover/recover-group/quota-group | `router/account_pool_test.go` 权限/当前句柄/精确作用域/审计；`TestAccountGenerationCurrentOwnerInheritance` | 无桌面管理验收 | `TestAccountManagementConfiguredDatabases` 与 owner/代际三DB；Chrome真实点击、确认、stale/失败刷新、独立权限，API夹具 |
| `embeddings.create`：text-embedding-v4/1024 | `TestEmbeddingFixedSpaceHTTP`、`TestKnowledgeCompatibleHTTP`，模型/维度/数值/索引/别名、完整用量、一次结算 | 实际 OpenAI 文档与查询客户端；vec0 float[1024] cosine 与 FTS5，逆序供应商索引恢复 | 网关与消费者使用独立临时SQLite；固定空间编辑浏览器保留原始JSON/大整数/零/false |
| `rerank.create`：Ali 原生/规范 | `TestKnowledgeCompatibleHTTP`，原生嵌套请求、原始索引/有限分数/用量验证 | 实际 DashScope client；重排原始索引映射 ChunkID 后SQL回读页/框/字符范围 | 消费者SQLite持久引用join；没有MySQL/PG整条消费者链声明 |
| `ocr.jobs.create/retrieve`、`ocr.resources.retrieve` | `TestOCRPublicFixedConsumerAndCanonical`、完整请求体限制与service/ocr生命周期/下载/终态拒绝恢复；真实401/429及不确定结果边界 | 实际 Paddle Read、提交/poll/JSONL/image无Authorization下载；两页/实际PNG/精确定位与单次570总账 | `TestOCRConfiguredDatabases`、并发资金/回执与`TestOCRLogProjectionDatabases`三DB；Redis丢回执另有故障夹具；ClickHouse投递不支持 |
| `chat.completions.create`、`responses.create`：选定 Omni/视觉理解 | `TestDomesticUnderstandingHTTP`：PNG/JPEG、PCM WAV、视频载体/fps，真实混合/零用量和按次价格 | 未执行固定消费者音视频理解链 | 本地临时SQLite账本；不能推断供应商质量或所有模型支持 |
| `audio.transcriptions.create`：qwen3-asr-flash | `TestDomesticAudioHTTP`：真实PCM WAV、json/text、实际Token与秒数 | 无实际桌面ASR消费者 | 本地账本；翻译/时间戳/其他编码拒绝；无供应商识别质量验收 |
| `audio.speech.create`：speech-2.8-hd/turbo | `TestDomesticAudioHTTP`、MiniMax stream tests：原生非流式/MP3二进制流、拒绝/取消/写失败、字符量与按次价格 | 无实际桌面语音消费者 | 本地账本；私有/克隆音色拒绝；WAV真实响应元数据待采购验证 |
| `images.generations.create`、`images.edits.create`：qwen-image-2.0/pro | `TestDomesticImageHTTP`：真实multipart PNG/JPEG、1–6张、返回数/原生usage一致、按图价格与别名/组倍率 | 无实际桌面图片消费者 | 本地账本；旧异步分支/远程编辑图/mask等拒绝；真实多图供应商差异待验收 |
| `multimodal.embeddings.create`：qwen3-vl-embedding | `TestMultimodalEmbeddingHTTP`：规范/原生、实际图像与H264快照供应商读取、A429→B、固定fps1空间、真实T/V用量/预留、退款与撤销 | 尚无固定消费者MM索引链；文本旧库不修改 | `TestRelayMediaConfiguredDatabases`三DB；`TestRelayMediaHTTPImmutableRead` grants/range/revoke；Chrome wire/空间/媒体字段编辑 |

所有模型名称表示实现选择的协议分支，不保证某个地区、套餐、账号已购买或开通。媒体编码、字节、时长、图片数、视觉用量上限、公开origin/持久grant secret/对象存储及财务不确定状态详见运维指南；不要因局部fixture成功扩大支持范围。

## 待交付负责人处理

- 四操作清单修复和最终完整后端门禁已通过；后续如有代码修复，按实际影响补充验证并更新对应源码身份，不能把旧结果移用于未经验证的新代码。
- 全分支审查及其必要修复/验证；分项批准不能代替该门禁。
- 历史 Minor/parked 清单与全部有序 Ruling 保留在 W，交付前逐项决策并按要求披露。原生日志/测试负向场景噪声、TTS 成功响应遗留 Retry-After、ASR encoded-size夹具精度、Gemini未启用原生媒体验证缺口、实际消费者字体/日志限制均不能默默消失。
- 本地 fast-forward 前确认原工作区无受影响的跟踪修改；保留既有未跟踪法律文档/sidecar。Git 不复制忽略的 web/dist，需另行保存并复制/核对已验证资产；完整证据先归档，再清理托管工作树和仅本轮专用容器。
