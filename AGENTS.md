# AGENTS.md — Project Conventions for new-api

DO NOT send optional commentary

## Overview

This is an AI API gateway/proxy built with Go. It aggregates 40+ upstream AI providers (OpenAI, Claude, Gemini, Azure, AWS Bedrock, etc.) behind a unified API, with user management, billing, rate limiting, and an admin dashboard.

## Tech Stack

- **Backend**: Go 1.22+, Gin web framework, GORM v2 ORM
- **Frontend**: React 19, TypeScript, Rsbuild, Base UI, Tailwind CSS
- **Databases**: SQLite, MySQL, PostgreSQL (all three must be supported)
- **Cache**: Redis (go-redis) + in-memory cache
- **Auth**: JWT, WebAuthn/Passkeys, OAuth (GitHub, Discord, OIDC, etc.)
- **Frontend package manager**: Bun (preferred over npm/yarn/pnpm)

## Architecture

Layered architecture: Router -> Controller -> Service -> Model

```
router/        — HTTP routing (API, relay, dashboard, web)
controller/    — Request handlers
service/       — Business logic
model/         — Data models and DB access (GORM)
relay/         — AI API relay/proxy with provider adapters
  relay/channel/ — Provider-specific adapters (openai/, claude/, gemini/, aws/, etc.)
middleware/    — Auth, rate limiting, CORS, logging, distribution
setting/       — Configuration management (ratio, model, operation, system, performance)
common/        — Shared utilities (JSON, crypto, Redis, env, rate-limit, etc.)
dto/           — Data transfer objects (request/response structs)
constant/      — Constants (API types, channel types, context keys)
types/         — Type definitions (relay formats, file sources, errors)
i18n/          — Backend internationalization (go-i18n, en/zh)
oauth/         — OAuth provider implementations
pkg/           — Internal packages (cachex, ionet)
web/           — Frontend (React 19, Rsbuild, Base UI, Tailwind)
  src/i18n/    — Frontend internationalization (i18next, en/zh/zh-TW/fr/ru/ja/vi)
```

## Internationalization (i18n)

### Backend (`i18n/`)
- Library: `nicksnyder/go-i18n/v2`
- Languages: en, zh

### Frontend (`web/src/i18n/`)
- Library: `i18next` + `react-i18next` + `i18next-browser-languagedetector`
- Languages: en (base), zh (fallback), zh-TW, fr, ru, ja, vi
- Translation files: `web/src/i18n/locales/{lang}.json` — flat JSON, keys are English source strings
- Usage: `useTranslation()` hook, call `t('English key')` in components
- CLI tools: `bun run i18n:sync` (from `web/`)

## Rules

### Common Code Quality

- New code should stay direct and readable. Prefer early returns, clear branches, and well-named local variables to deep nesting or layered control flow.
- Minimize nested function definitions. Use them only when required by a callback API or when keeping the closure local is clearly simpler than adding another symbol.
- Avoid adding package-level or module-level helper functions that have only one caller and do not express a stable business concept. Inline that logic at the call site instead.
- A separate function is appropriate when it represents reusable behavior, a required interface/framework callback, an exported API, a test fixture, or complex business logic that deserves direct tests.
- If a single-use helper is kept, its name must describe a durable domain concept rather than a mechanical step extracted only to shorten the caller.

### Backend Rules

**JSON package:** All JSON marshal/unmarshal operations MUST use the wrapper functions in `common/json.go`:

- `common.Marshal(v any) ([]byte, error)`
- `common.Unmarshal(data []byte, v any) error`
- `common.UnmarshalJsonStr(data string, v any) error`
- `common.DecodeJson(reader io.Reader, v any) error`
- `common.GetJsonType(data json.RawMessage) string`

Do NOT directly import or call `encoding/json` in business code. `json.RawMessage`, `json.Number`, and other type definitions from `encoding/json` may still be referenced as types, but actual marshal/unmarshal calls must go through `common.*`.

**Database compatibility:** All database code MUST work with SQLite, MySQL >= 5.7.8, and PostgreSQL >= 9.6 simultaneously.

- Prefer GORM methods (`Create`, `Find`, `Where`, `Updates`, etc.) over raw SQL.
- Let GORM handle primary key generation; do not use `AUTO_INCREMENT` or `SERIAL` directly.
- Standard `SELECT ... FOR UPDATE` row locks built with GORM query methods in `model/` MUST use `lockForUpdate(tx)`. Do not use the legacy GORM v1 pattern `tx.Set("gorm:query_option", "FOR UPDATE")`, because GORM v2 silently ignores it and no lock is acquired. Do not duplicate `clause.Locking{Strength: "UPDATE"}` at call sites; the shared helper emits `FOR UPDATE` for MySQL/PostgreSQL and skips it for SQLite, where the syntax is unsupported. Dialect-specific locking with different semantics (for example, a MySQL next-key/gap lock) may use raw SQL only behind explicit database-type branches with valid fallbacks for every supported database.
- When raw SQL is unavoidable, account for dialect differences:
  - PostgreSQL uses `"column"` quoting, while MySQL/SQLite use `` `column` ``.
  - Use `commonGroupCol`, `commonKeyCol` from `model/main.go` for reserved-word columns like `group` and `key`.
  - Use `commonTrueVal`/`commonFalseVal` for boolean values.
  - Use `common.UsingMainDatabase(...)` for primary database branches and `common.UsingLogDatabase(...)` for log database branches.
- Do not use database-specific features without cross-DB fallback, including MySQL-only functions, PostgreSQL-only operators, SQLite-unsupported `ALTER COLUMN`, or database-specific JSON column types without a `TEXT` fallback.
- Migrations must work on all three databases. For SQLite, use `ALTER TABLE ... ADD COLUMN` instead of `ALTER COLUMN` (see `model/main.go` for patterns).
- Avoid GORM boolean default tags such as `gorm:"default:true"` when the default is a business rule already enforced by code. MySQL and PostgreSQL can normalize boolean defaults differently, causing GORM `AutoMigrate` to repeatedly issue `ALTER TABLE` on restart. Prefer setting these defaults in request/model normalization, hooks, constructors, or service logic; do not replace `default:true` with `default:1` unless the behavior is verified across SQLite, MySQL, and PostgreSQL.

**Relay and provider behavior:**

- When implementing a new channel, confirm whether the provider supports `StreamOptions`; if supported, add the channel to `streamSupportedChannels`.
- For request structs parsed from client JSON and re-marshaled to upstream providers, optional scalar fields MUST use pointer types with `omitempty` (for example, `*int`, `*uint`, `*float64`, `*bool`).
- Preserve explicit zero values in upstream relay request DTOs: absent client JSON fields must become `nil` and be omitted, while explicit `0`, `0.0`, or `false` values must remain non-`nil` and be sent upstream.
- Avoid non-pointer scalars with `omitempty` for optional request parameters, because zero values will be silently dropped during marshal.

**Billing expression system:** When working on tiered/dynamic billing (expression-based pricing), MUST read `pkg/billingexpr/expr.md` first. It documents the design philosophy, expression language, full architecture, token normalization rules, quota conversion, and expression versioning. All billing expression changes must follow that document.

**Billing safety invariants:** Quota/billing code MUST never produce a negative charge (a credit) from arithmetic overflow or unvalidated input. Apply defense in depth:

- Every user-controlled quantity that becomes a billing multiplier (image `n`, video `seconds`/`duration`, resolution/quality ratios, batch counts) MUST be bounded before it reaches quota calculation. Reject out-of-range values at request validation with a 400. Existing bounds: `dto.MaxImageN` for image generation count, `relaycommon.MaxTaskDurationSeconds` for task video duration, `maxTokensLimit` (`relay/helper/valid_request.go`) for `max_tokens`-family fields on every relay format (OpenAI, Claude, Gemini, Responses). Reuse these constants instead of introducing new ad hoc limits for the same concepts. When adding a new relay format or request DTO, bound its max-tokens and count fields in its validator from day one.
- Watch for validation bypass paths: passthrough fields (e.g. `Extra["parameters"]`), task `metadata` maps, and multipart form fields can carry the same quantities around the standard DTO validation. Any adaptor that reads a multiplier from such a path must enforce the same bound (or clamp) locally.
- Durations parsed from media metadata are user/upstream-controlled too: audio file headers (transcription token counting, TTS response duration) and upstream deduction numbers (e.g. Kling `FinalUnitDeduction`) can claim absurd values. Convert them with saturation before they become token counts.
- Never convert a computed quota or token count to `int` with a bare cast like `int(float64(quota) * ratio)`, `int(math.Round(...))` on unbounded input, or `int(decimal.IntPart())`. All quota rounding/conversion is centralized in `common/quota_math.go`; use those helpers: `common.QuotaFromFloat` (truncating) for float products, `common.QuotaRound` (half-away-from-zero) where rounding is intended, and `common.QuotaFromDecimal` for decimal products. `billingexpr.QuotaRound` delegates to `common.QuotaRound`. Do not reintroduce local conversion helpers or bare casts. Saturation bounds are int32 because quota columns (user/token/log) are 32-bit integers in the database, and every clamp/NaN fallback is logged via `common.SysError` since a single request should never approach those bounds.
- Saturation events are also audited: each helper has a `*Checked` variant (`common.QuotaFromFloatChecked` / `QuotaRoundChecked` / `QuotaFromDecimalChecked`) that additionally returns a `*common.QuotaClamp` when clamping occurred. Billing paths that compute a charge capture that clamp onto `relayInfo.QuotaClamp` (or thread it into task settlement) and, right before writing the consume/task log, call `attachQuotaSaturation` (in `service/log_info_generate.go`) which nests the marker under the log's `other.admin_info.quota_saturation` and emits a request-correlated `logger.LogWarn`. Nesting under `admin_info` makes it admin-only for free (non-admin log views strip `admin_info`). When adding a new billing path, use the `*Checked` variant and surface the clamp the same way so the anomaly stays auditable in both the admin log UI and backend logs.
- Multiplier maps go through `types.PriceData.AddOtherRatio`, which rejects non-positive, NaN, and +Inf ratios. Do not write to `PriceData.OtherRatios` directly, and do not weaken these guards.
- Pre-consume (预扣费) and settle (结算/差额) must both be safe: a saturated oversized quota must fail pre-consume with insufficient-quota, never silently wrap. When adding a new billing path (new relay format, new task platform, new adjustment hook), trace the full chain — validation → EstimateBilling/OtherRatios → quota conversion → pre-consume → settle/refund — and confirm each step preserves these invariants.
- Fields parsed into unsigned types (`*uint`) accept huge positive JSON numbers (e.g. `18446744073686646784`, a wrapped negative); a `>= 0` check is not sufficient, an upper bound is mandatory.
- Regression tests for these invariants belong with the boundary they protect (request validators, converter helpers). See `relay/helper/openai_image_request_test.go`, `relay/common/relay_utils_test.go`, and `common/quota_math_test.go` for the expected style.

**Backend test quality:** Backend tests must protect real behavior, API contracts, billing/accounting invariants, data compatibility, or regression paths.

- Do not add tests that only improve coverage numbers, prove that code happens to run, or lock in implementation details without a user-visible or cross-module contract.
- Avoid fake fuzz/stress/smoke/performance tests built from random inputs, large loop counts, sleeps, timing comparisons, or log-only assertions.
- Avoid duplicate tests that exercise the same branch with different names but no new invariant.
- Avoid tests that force incorrect provider/protocol semantics into production code.
- Avoid tests that assert private constants, select-field lists, helper internals, or file layout when observable behavior is already covered elsewhere.
- Prefer deterministic table tests with explicit inputs and exact expected outputs.
- When tests need database, request context, user group, settings, or cache state, initialize that state explicitly inside the test fixture.
- New or substantially rewritten Go backend tests MUST use `github.com/stretchr/testify/require` for setup and fatal assertions, and `github.com/stretchr/testify/assert` for non-fatal value checks.
- Avoid hand-written assertion helpers unless they encode a reusable project-specific invariant.
- When cleaning tests, preserve meaningful regression coverage. If a deleted test covered a real contract indirectly, replace it with a smaller test that asserts that contract directly.

### Frontend Rules

- Use `bun` as the preferred package manager and script runner for the frontend (`web/`):
  - `bun install` for dependency installation
  - `bun run dev` for development server
  - `bun run build` for production build
  - `bun run i18n:*` for i18n tooling
- Frontend UI text must support i18n with `i18next`/`react-i18next`. Use flat JSON locale files in `web/src/i18n/locales/{lang}.json`, with English source strings as keys.
- In React components, use `useTranslation()` and call `t('English key')` for user-facing text.
- Follow `web/AGENTS.md` for detailed frontend conventions, including TypeScript, component structure, styling, accessibility, testing, and build checks.

### Project Governance

**Project branding and upstream attribution**

LLMRouter is a deeply customized downstream product and may use its own
branding on product-facing surfaces, including page titles, meta tags,
navigation, homepage copy, application names, deployment images, and
user-facing documentation.

References to the upstream New API project and QuantumNous must remain
where they serve legal, licensing, copyright, historical, or attribution
purposes, including LICENSE, NOTICE, THIRD-PARTY-LICENSES, dependency
metadata, source attribution, and the About page.

Product-facing New API branding may be replaced with LLMRouter branding
as long as required upstream attribution and license notices are preserved.

**Pull requests:** When creating a pull request:

- First compare the current git user (`git config user.name` / `git config user.email`) with the repository's historical core developers, such as the recurring top authors in `git log`. Do not change git config.
- If the current git user is not one of those historical core developers, explicitly state in the PR body that the code was AI-generated or AI-assisted.
- Always use the repository PR template at `.github/PULL_REQUEST_TEMPLATE.md` when drafting the PR title/body. Preserve the template structure and fill in the relevant sections instead of replacing it with an ad hoc format.

## Multi-Agent Collaboration

**Codex、犇马（SuperWagie）、WorkBuddy** 三路可同时在本仓库开发：共享一份 `.git`，各自独立 worktree、分支、本地运行实例。

三方交接点是**文件**，不依赖任一客户端的 API：

- 代码交接：`main` 分支
- 状态记录：`~/项目/codexprojects/U-L-S 综合验收/OPERATIONS.md`。**发布方追加新段落，不改旧段落**；总控保留验收段落。三方不并发写同一段落。
- 交接日志：同目录 `HANDOFF.md`。**append-only**：写入前先读文件尾部，只在末尾追加；**禁止整文件覆盖或重写模板**。该文件无技术写保护（2026-11-10 22:19 曾被整体重写丢 3 条记录），整文件写入前必须先确认没有他人条目。

### 工作区与分支

- 用独立 git worktree，不要直接改主工作区 `~/项目/codexprojects/llmrouter`。worktree 根目录读 `~/.codex/config.toml` 的 `git-worktree-root`（当前 `/Volumes/codex/new-worktrees`）；外置卷不可用时停下说明，不回退内置盘。
- 目录用途带所有者前缀：`codex-`、`sw-`（犇马）、`wb-`（WorkBuddy）。

| 客户端 | 分支前缀 |
|---|---|
| Codex | `codex/*` |
| 犇马 | `sw/*` |
| WorkBuddy | `wb/*` |

- 分支前缀即所有权，只在自己前缀下提交。`agent/*` 是历史遗留，不再使用。
- **三方都不直接推 `main`**，只走分支 + 由人合并。合并时机：每轮联调开始前，由人在 `main` 上合入已验收的分支；合完各客户端 `fetch` 后从 `main` 拉新分支。

### 提交身份

分支前缀只是名义所有权，提交作者必须也能区分客户端。每个客户端在自己的 worktree 设 local 作者（不改全局配置）：

| 客户端 | user.name | user.email |
|---|---|---|
| Codex | `Codex` | `codex@local` |
| 犇马 | `SuperWagie` | `sw@local` |
| WorkBuddy | `WorkBuddy` | `wb@workbuddy.local` |

```sh
git config user.name "WorkBuddy"
git config user.email "wb@workbuddy.local"
```

已推送的提交不改写。

### 推送陷阱（必须知道）

本仓库有两个 remote：

- `llmrouter` → 自有仓库 `NeoMei/llmrouter`（fork 主干）
- `origin` → 上游 `QuantumNous/new-api`（只读，无写权限）

`branch.main.remote` 已设为 `llmrouter`，但其他分支**不会**自动用自有仓库。推送时**必须显式指定 `llmrouter`**：

```sh
git push llmrouter <branch>
```

不要用 `git push`（可能命中 `origin` 并报权限拒绝），不要 `git push -u origin`（会建立到上游的错误跟踪）。

### 本地运行

默认行为（**重要，别按共享 PG/Redis 假设规划**）：

- `SQL_DSN` 为空时走 **SQLite**，库文件在 repo 根 `one-api.db`（由 `SQLITE_PATH` 指定，已 gitignore）——每个 worktree 天然一份，无需分配库名。
- `REDIS_CONN_STRING` 为空时走**内存缓存**，不碰 Redis。

只有显式配置 `SQL_DSN` / `REDIS_CONN_STRING` 才会使用共享 PG/Redis，此时按下列分配值，且不要对同一套数据库并发跑迁移：

| 客户端 | DB | Redis db | PORT |
|---|---|---|---|
| Codex | 默认 SQLite | 内存 | 3000 |
| WorkBuddy | SQLite（默认）；共享模式用 `llmrouter_wb` | 内存；共享模式 db 1 | 3020 |
| 犇马 | SQLite（默认）；共享模式用 `llmrouter_sw` | 内存；共享模式 db 2 | 3010 |

并行起服务时用上表 `PORT`，不要都占 3000。

### 依赖与环境

- `node_modules`、`bin`、`.codegraph`、`one-api.db` 不跨 worktree 共享，每个 worktree 自己准备。
- `.env` 若存在只属于该 worktree：不复制、不提交。

### 交接

- 收工前把改动提交到自己分支并 `git push llmrouter <branch>`。
- 接手前 `git fetch llmrouter`，确认 `main` 与 `llmrouter/main` 一致。
- 在 `HANDOFF.md` 追加一条，写清四件事：做了什么、没做什么、下一步、未合并的依赖分支。不改别人写过的段落。
