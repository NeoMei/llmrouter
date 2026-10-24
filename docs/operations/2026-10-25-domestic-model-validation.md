# 国产模型账号池：2026-10-25 本地验证记录

本文对应[接口与运维指南](domestic-model-account-pools.md)，记录本地源码验收，不是生产发布证明。分项验证和全分支独立审查后，以一次修复处理两项 Important、两项 Minor，范围复审确认四项均解决且无新 Critical/Important/Minor。修复后精确源码的完整后端门禁通过，受影响前端检查与生产构建通过；未受影响的发布契约证据复用。所有早期失败保留。原项目已完成本地快进、231个资产逐摘要复制和合入后全量普通测试；完整证据与产物的持久交付索引见下文。

## 源码与证据身份

首轮全量检查 HEAD 为 `3dada17c87dbe52eb4ea8a3089a6efd642946a32`，Git tree 为 `b9bf687651b61f761416f6d1b73f130e911157eb`，开始时工作区干净。相对最后生产代码提交 `1a70e2dc1` 只有文档提交。前端产物来自该生产代码的最终 `bun run build`（2.35 秒），231 个产物文件的逐文件 SHA256 保存在 `source-before.json`；没有用空白占位目录满足 Go embed。最终修复前的完整后端验证对应 `6a2db5a395278b9d2b8c15b444acc99e8264e3e8`（tree `8c38555b2a23d04ac75e455605f2967b68a26f2e`），相对首轮只追加 `tests/contracts/provider_fixtures.json` 中遗漏的四操作；没有生产代码变更。`source-final-before.json` 和 `source-after.json` 证明验证前后 HEAD 相同、231 个资产摘要完全一致，未提交差异只有本文与运维指南。

下面的 `W` 指工作树中的 `.superpowers/sdd/2026-10-24-domestic-multimodal-account-pools`。本轮证据在 `W/evidence/task-10c/`，每个命令分别保留 `.command.json`（cwd/argv/环境/开始时间/HEAD）、`.stdout.log`、`.stderr.log`、`.exit`、`.result.json`（结束时间/耗时）。后端另有 `.phases.log`，通过仅记录命令的 Bash DEBUG trap 留存阶段时间；没有测试过滤或套件排除。完整W和构建产物的持久交付目录为 `/Users/neomei/.codex/artifacts/2026-10-25-llmrouter-domestic-model-account-pools`。其中 `workspace/` 对应完整W（包括早期失败、审查overlay、消费者二进制/来源、SQLite、浏览器trace），`web-dist/` 保存实际构建产物；根目录 `README.md` 与 `workspace-manifest.json`、`web-dist-manifest.json` 记录最终归档状态及逐文件完整性。清理仅在这些校验通过后执行。

环境为 darwin/arm64、Go1.26.10、CGO_ENABLED=1、Bun1.4.3。后端使用 `GOMAXPROCS=4 GOFLAGS=-p=4` 限制本机并行资源，不减少测试。两项 `LLMROUTER_ACCOUNT_POOL_MYSQL_DSN` / `LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN` 指向专用临时 MySQL5.7.44（65380）/PostgreSQL9.6（65381），完整夹具 DSN 仅在原始执行记录保存。未接触用户 `unihub-dev-*` 服务。

## 命令与结果

| 命令（注明 cwd） | 结果 | 原始证据 |
| --- | --- | --- |
| `bun run lint`（web） | PASS，136 个 warning，全部来自相对原交付基线 `50fd8d427` 未改动的文件；本分支改动文件 0 warning | `task-10c/web-lint.*`、`lint-warning-classification.json` |
| `bun run test`（web，默认完整发现） | PASS，124 测试、22 suites，0 失败/取消/跳过 | `task-10c/web-test.*` |
| `GOTOOLCHAIN=go1.26.10 bash scripts/verify-backend.sh`（根目录，另有上述并行/DSN 环境） | 修复前 PASS，96.868 秒，全部六阶段通过；首轮 FAIL52.152 秒保留；修复后结果见下节 | `task-10c/backend-final.*`、首轮 `backend.*` |
| `python3 -m unittest scripts/release_ci_contract_test.py -v`（根目录） | PASS，7 测试 | `task-10c/release-ci.*` |
| `bash scripts/release_config_test.sh`（根目录） | PASS，夹具配置与 `--check` 预检 | `task-10c/release-config.*` |
| `bash scripts/release_scripts_test.sh`（根目录） | PASS，包括 `restore_test.sh` 夹具完整性测试 | `task-10c/release-scripts.*` |

首轮唯一失败为 `tests/contracts/completeness_test.go:37` 的 `TestEverySupportedCapabilityHasProviderFixture`：能力注册表为 32 个操作，旧清单仅 28 个，遗漏 `multimodal.embeddings.create`、`ocr.jobs.create`、`ocr.jobs.retrieve`、`ocr.resources.retrieve`。实际对应 HTTP 夹具已存在，需补齐正确映射；不能通过删除能力、忽略测试或只声称 owner 测试通过来关闭全量门禁。首轮失败后未继续执行 vet、race、build、额外 onboarding。独立审查通过的清单修复后，以相同环境完整重跑原脚本，最终 exit0、stderr0 字节。没有重复已通过的前端/发布检查。普通 Go test 合法复用缓存；本轮 race 为实际执行，model66.739秒、router18.231秒、service/ocr5.253秒，没有race诊断。首次非缓存model44.454秒及既有逐项三数据库证据保留，不因缓存摘要重复已通过套件。

修复前阶段时间（UTC，Bash阶段日志精度1秒；完整父进程起止为19:34:36.989–19:36:13.865）：

| 阶段 | 开始时间 | 完成依据 |
| --- | --- | --- |
| tracked Go gofmt | 19:34:37 | 无未格式化文件，进入test |
| `go test ./...` | 19:34:37 | 19:34:38进入vet |
| `go vet ./...` | 19:34:38 | 19:34:40进入race |
| `go test -race ./...` | 19:34:40 | 19:35:56进入build |
| `go build -trimpath ./...` | 19:35:56 | 19:36:11进入额外onboarding |
| `go test ./internal/onboarding/... ./cmd/llmrouter-onboard/... -count=1` | 19:36:11 | 三包PASS，父命令exit0 |

发布契约测试没有启动生产 Compose、读取生产配置或部署。`release_gate_test.sh` 与发布门禁实现相对此次基线未改动，本轮没有另跑它；早期已核对的 Docker Go1.26.10 验证也没有因状态汇报重复执行。

修复前的分项证据保留如下；本次改变的 UI 源码由后文最终修复检查覆盖，其余未改动检查继续复用：

| 检查 | 修复前结果与证据 |
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

## 全分支审查、一次修复与最终精确验证

全分支审查 `50fd8d427..dc17e2fab66c2f6cb046ec8579a8bd738bfa3f0e` 发现 0 Critical、2 Important、2 Minor。一次修复提交 `0228da9ceb60c9affcf1c6c34b1217be683165ba` 处理四项；`W/final-scoped-rereview.md` 对该精确修复范围复审批准，四项均 ADDRESSED，新发现 0 Critical/Important/Minor。早期全量通过没有覆盖这些新增回归边界，故没有用旧结果替代修复后验证。

| 项目 | 已验证的修复行为 | 证据 |
| --- | --- | --- |
| I1 旧路由别名健康作用域 | 健康准入前解析实际模型；同模型别名共享冷却、链终点一致、不同模型独立；目录目标不被再次映射，旧公开/计费身份不变。API/UI 保留已配置一跳目标并增加有效旧路由终点，不扩大任意模型权限 | `final-fix/alias-race-final.*`、`mapping-race-final.*`、`owner-go-final.*`、`verified-ui-browser/` |
| I2 OCR 结果物理清理后的状态 | 拥有者规范/兼容 GET 在到期及实际 purge 后均410；既有结算/额度/消费日志/回执保持不变，供应商工作不增加；真实查询故障不伪装成到期 | `TestOCRRenewableGrantProjectionAfterRestartAndRetention`、`TestOCRPublicFixedConsumerAndCanonical`，`final-fix/owner-race.*`及本轮完整套件 |
| M1 原生成功响应退避头 | 流式/非流式 TTS 在实际响应头提交前清除旧 Retry-After，其他前输出原生成功共用完成接口；真实最终429保留退避，HTTP200本身不冒充语义成功 | `TestNativeSuccessClearsOnlyValidatedBackoff`、真实音频 committed-header断言，`final-fix/owner-race.*` |
| M2 ASR 编码体积夹具 | 8,388,608字节真实PCM WAV由实际解析器验证为约95.108秒，multipart低于10MiB而编码载体11,184,834字节；特定编码超限错误且零发送/预扣 | `final-fix/carrier-guard-removal.*`预期失败、`carrier-guard-present.*`通过；仅移除目标保护会真实发送并扣34，独立证明该边界 |

最终认证 HEAD `0228da9ceb60c9affcf1c6c34b1217be683165ba`，tree `df0548402ee934b0b25fb406bec59c058480c20b`，起始工作区干净。前端 `bun run --cwd web build` 构建2.28秒，231文件、57,413,818字节；`final-fix/final-source-identity.json` 将提交前构建的源码清单绑定到该提交，构建产物清单SHA256为 `b1c3fec4d1c74ce757063265d15d7effd63771fbe3d8e3f4aabcdbc4b838cefa`。`post-final-fix/source-before.json`/`source-after.json`逐文件核对所有产物与作者清单一致，验证期间 HEAD/tree/资产不变，结束后仅两份本文档有未提交编辑；后续纯文档提交不改变本次代码与资产身份。

最终唯一完整命令仍为 `GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=4 bash scripts/verify-backend.sh`，根目录 cwd、相同两项专用最小版本数据库 DSN、消费者 opt-in unset。新原始记录独立放在 `W/evidence/post-final-fix/`，没有覆盖早期失败或通过记录。结果 **PASS，exit0，145.233秒，stderr0字节**。实际普通 model46.116秒/router2.101秒/OCR1.047秒；race66.949秒/19.176秒/5.327秒，无race诊断。额外 onboarding三包亦通过。默认允许有效Go缓存，未强制重跑既已通过的不变用例。

| 最终阶段 | 开始时间UTC | 完成证据 |
| --- | --- | --- |
| tracked gofmt / `go test ./...` | 20:08:11 | 格式通过，完整test后20:09:03进入vet |
| `go vet ./...` | 20:09:03 | 20:09:05进入race |
| `go test -race ./...` | 20:09:05 | 20:10:23进入build |
| `go build -trimpath ./...` | 20:10:23 | 20:10:33进入额外onboarding |
| `go test ./internal/onboarding/... ./cmd/llmrouter-onboard/... -count=1` | 20:10:33 | 父命令20:10:36.325结束exit0；起点20:08:11.083 |

最终修复作者另完成受影响前端3个单元测试、typecheck、lint（仍136条未改文件警告）、format1006文件、3个真实Chrome界面场景及上述新构建；原始命令和source manifest在 `final-fix/ui-*-clean.*`、`verified-ui-browser.*`、`production-build.*`。早期124项默认前端测试、七语言/copyright/Knip与三项发布契约结果保留，未因状态汇报重复执行；新增作用域UI由作者精确检查覆盖。浏览器API仍为隔离夹具，并非部署后的浏览器到DB验收。

本次共 **54条 Ruling**，按原顺序保存。第54条明确旧路由链终点与原目录一跳作用域的最小兼容投影；早期审查的53条是当时的历史计数。全分支审查已决定其余 parked项：额外同组逆序测试和夹具重构保留为后续增强；预期负向SQL/Gin/饱和审计输出不抑制；Gemini原生媒体未启用边界、供应商真实质量/容量、字体渲染和桌面/完整摄入/三DB消费者全链等限制继续保留。TTS退避头及ASR夹具精度现已修复，不再列为未完成项。

## 本地交付记录

- 原项目 `/Users/neomei/项目/codexprojects/llmrouter` 的 `codex/wechat-native-pay` 从已核对的 `50fd8d427` 本地快进到 `6ed213439974c493e4b34ee1b38d26051d656341`；后续交付记录提交仅改变文档。合入前后均没有跟踪文件修改，三个原有未跟踪文件保持原摘要。
- Git不复制忽略的web/dist：另行复制231文件、57,413,818字节，并与最终构建清单逐项SHA256一致。原项目使用这些真实资源再次运行 `go test ./...`，同样配置两专用最小版本数据库；PASS、exit0、56.412秒、stderr0字节。原始命令、完整输出、合入与复制记录在 `W/evidence/local-delivery/`；没有把工作树测试冒称为原项目测试。
- 全部54项实施决策及代价见[决策记录](2026-10-25-domestic-model-decisions.md)。原始审查、失败和全部证据随完整W保留。持久交付根目录的 `source-delivery.json`、`original-after.json` 和 README 记录最后文档提交及原项目状态，避免自引用提交哈希。
- 完整归档校验后才移除本计划临时目录、归档托管工作树并清理两项本轮专用数据库容器；不清理其他项目或既有用户服务。完成状态以持久交付README为准。
- 本次交付为本地代码集成；没有远端push、部署、真实采购账号调用或用户资料库迁移。真实账号可用容量和媒体质量仍需按运维指南逐项验收；账号池不能在所有账号均不可用时保证成功。
