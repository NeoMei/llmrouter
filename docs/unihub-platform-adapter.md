# UniHub platform Chat adapter

The adapter is opt-in. Existing `/v1` authentication, providers and local billing remain unchanged. Enable `UNIHUB_PLATFORM_ENABLED=true` with the explicit service origin, separate service token, enabled routing principal/group and limits shown in `.env.example`. No local wallet or token funds platform calls. The enabled principal must already belong to the fixed configured group. All gateway instances for the same UniHub service must share the same gateway database; the logical-call dispatch fence lives there.

## SuperWagie all-model account keys

A verified durable `uh_key_` may explicitly carry
`model_scope=llmrouter_available`, `pricing_source=llmrouter_native` and a
positive `credential_version`. This mode is restricted to SuperWagie API keys
funded in prepaid `CNY/6`. Set `UNIHUB_NATIVE_ROUTING_GROUP` to the existing
published native model group to enable it. Empty disables the mode. The
existing `selected` scope and task credentials retain their published-rate
contract below.

The public `/v1` dispatcher preserves the original key, actor, personal or team
account, instance, expiry and key budget. It never creates a native token or
uses the routing principal's money. It accepts one of `Authorization: Bearer`,
Claude `x-api-key` or Gemini `x-goog-api-key`, rejecting ambiguous headers and
query credentials. Gemini `streamGenerateContent?alt=sse` remains streaming.

`GET /v1/models` projects all currently enabled, priced native routes and
abilities in that group, without a second central model list or rate table.
The response includes `capability_metadata`, a catalog version, explicit
available/executable operation status and the verified account identity
headers. Adding or removing a native model changes discovery automatically.
Unknown protocol operations stay visible with a reason; catalog visibility
never grants an unsupported operation. The same catalog is available through
`POST /internal/v1/provider-catalog` using the dedicated catalog credential and
trusted origin-product, instance, actor and account binding. The caller cannot
choose a routing group, provider URL or prices.

Routes declaring only supported native image, embedding, rerank, audio or OCR
operations may keep `preferred_wires` empty. These operations use their fixed
endpoint protocol and do not invent a Chat wire. The catalog and resolver
apply the same explicit operation allowlist; absent, unknown or mixed Chat
operations still require the original preferred-wire contract. Request format,
relay mode and declared operation must all agree before native reservation.

The gateway computes the reservation and final native quota using LLMRouter's
existing model, group, capability and metering rules. The immutable price
snapshot and quota-to-cash conversion are hashed before
`/internal/v1/calls/native-reserve`. The returned hold must bind the original
key version, task, actor, account, product, instance, model, wire operation and
complete quote. Key authority and the selected native route are checked again
immediately before the durable dispatch fence. Removal, key revocation or lost
team membership prevents a supplier request even after reservation.

Final native quota is converted once using the frozen rational rate and
cumulative ceiling. A zero-priced model creates a zero-value reservation and
receipt. A charge above the hold first uses a budget-checked top-up; rejection
leaves a recoverable pending settlement. It is never silently capped and never
charged to a platform-difference account. Terminal receipts must return the
exact charged amount. Ambiguous reservation responses, interrupted transport
or missing final token usage remain pending/unknown and cannot replay a
supplier request. Recovery only retries durable monetary intents.

Generic native Chat, Claude Messages, Gemini, Responses, embedding, rerank,
image and audio handlers retain their established provider wire format. The
DashScope image endpoint accepts the native Qwen request directly. For OpenAI
image generation, declared pixel limits require a current-candidate typed Qwen
request with an explicit `widthxheight` size. The request must fit the declared
pixel and image-count limits before reservation. The native DashScope endpoint
applies the same limits to its normalized request and enforces the declared raw
request byte limit. Declared formats require verified metadata; a URL suffix
does not prove the output format. A candidate change clears the prior typed request; an unverified provider cannot inherit that proof. Qwen's
actual response width, height and image count must also fit the frozen route
limits before returning image URLs or recording native usage. Missing output
metadata remains an error; native size normalization inside those bounds keeps
its existing actual-usage semantics. PaddleOCR
jobs preserve exact key ownership through asynchronous polling, settlement and
text-document delivery. A direct PaddleOCR channel may use single-member pool
mode only when its actual credential list contains exactly one nonempty enabled
member. The native account selector still enforces cooldown and persists the
member identity before submission. Polling resolves that original member and
checks its frozen key hash; disabling or replacing the credential never selects
another account for the old task. Multi-member pools and gateway profiles remain
unavailable for managed-native OCR. The official `PaddleOCR-VL-1.6` endpoint
`https://paddleocr.aistudio-app.com/api/v2/ocr/jobs` also freezes the exact
`https://paddleocr-store.bj.bcebos.com` result origin. Existing native jobs whose
snapshot contains only the official API origin may read that same bucket only
after checking the original native call, quote, provider job, enabled single
member, owner and key hash. This compatibility check does not rewrite the frozen
snapshot, submit a new job, or grant the exception to custom endpoints. The
protected download client still refuses redirects and private-network targets.
Selected-scope tariffs keep their existing
single-key restriction. Stored Responses continuations (`store=true` or
`previous_response_id`, Responses `conversation`, Gemini `cachedContent` or
Claude `container`) are refused until a separately scoped native history
owner exists. Stateless Responses requests explicitly set `store=false`; the
non-financial routing principal is never used to authorize
private results. Provider-owned file, vector-store and container references,
Gemini file references and background Responses jobs are also refused until
scoped ownership exists. Inline content, ordinary text and function argument
payloads remain available; these checks inspect protocol fields only.

## Selected-scope HTTP contract

`POST /platform/v1/chat/completions` accepts an opaque model credential in `Authorization: Bearer uh_model_...`, a stable `Idempotency-Key` for **each logical model request**, and `X-UniHub-Task-ID`. A subsequent tool loop uses a new call ID; provider retries within the original HTTP call reuse the same logical call with distinct persisted attempts. Both IDs allow 1–128 ASCII alphanumeric / `_ . : -` characters, beginning with an alphanumeric character. The gateway returns `X-UniHub-Call-ID` once admitted.

Platform requests reject duplicate JSON keys at every nesting level and require exact canonical DTO field names on protocol-owned objects, including nested messages, stream options and tools. Unknown or case-alias fields are rejected before reservation. User-defined keys inside function parameter schemas and metadata retain their case-sensitive meanings.

Function tools support the canonical optional `function.strict` boolean. Explicit `false` and `true` survive the shared OpenAI DTO and upstream serialization; absence stays absent. Platform requests reject null, other types, aliases and duplicate `strict` keys before financial reservation. This field is used by the actual OpenCode 2.1.3 SDK, so it is covered by a real router-to-provider regression as well as DTO round-trip tests.

Provide a published model and explicit positive `max_tokens` or `max_completion_tokens` within the configured output limit. The adapter bounds body bytes by both `UNIHUB_MAX_BODY_BYTES` and half `UNIHUB_MAX_INPUT_TOKENS`, conservatively reserving the full configured input limit plus output maximum. It uses the highest published rational input/cache price and output/reasoning price, sums exactly and rounds upward once. This is a hold, not an estimated final charge. `UNIHUB_MODEL_RATE_IDS` optionally pins each model to a published immutable rate ID; an ambiguous unpinned published rate fails closed. There is no invented production tariff or client-specified hold amount.

The initial supported provider is standard OpenAI Chat (channel type 1), JSON or SSE, with text messages, text-only content arrays and function tools. Streaming requests force `stream_options.include_usage=true`. Multimodal/built-in tool surcharges, alternate protocols/resources, automatic Responses conversion, passthrough bodies, channel parameter/header overrides and injected system prompts are rejected until their billing semantics are explicitly implemented. Server-side channel model mapping and ordinary existing channel selection/retry remain available. Browser headers/body cannot choose the payer or routing principal/group.

## 统一 Key 的操作边界

本节描述的是**当前实现边界**，不是能力承诺。统一 `uh_key_` API Key 的 `/v1` 分发由 `router.SetManagedKeyDispatch` 完成：它通过显式的 method/path 分支把请求改写到对应的 `/platform` 处理器，只有能映射到已注册平台操作的路径才会继续。没有映射到的路径在 `target == ""` 时直接返回 HTTP 501，响应体为 `{"error":{"code":"unified_operation_unsupported","message":"this operation is not available for unified API keys"}}`（见 `router/managed_key.go`）。这里的「白名单」就是这些显式分支，不是可配置的通配集合；本节不把任何未验证能力写成已支持。

### 白名单内允许的操作

下表为 `router/managed_key.go` 中 POST `switch path` 显式列出的路径，以及同文件按前缀/别名规则映射的路径。GET `/v1/models`、`/v1/models/:model` 改写到 `/platform/v1/models` 用于发现；任务类 GET 读取（`/v1/videos/:id`、`/v1/images/edits/:id`、`/v1/ocr/:id`、`/api/v2/ocr/jobs/:id`、`/platform/v1/calls/:id`）在校验 `ManagedTask` 归属后改写，POST `/v1/videos/:id/cancel` 同理。

| 类别 | 允许的 POST 路径（统一 Key） | 依据 |
| --- | --- | --- |
| Chat / Claude / Gemini | `/v1/chat/completions`, `/v1/messages`, `/v1/responses`, `/v1/responses/compact`, `/v1beta/models/*` | `managed_key.go` POST `switch` 与 `HasPrefix(path, "/v1beta/models/")` 规则；`/v1/responses/compact` 对应 `managed_native.go` 的 `responses.compact` |
| Embedding / Rerank | `/v1/embeddings`, `/v1/rerank`, `/v1/multimodal/embeddings`, `/api/v1/services/rerank/text-rerank/text-rerank`, `/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding` | `managed_key.go` POST `switch` |
| Image / Audio / Video / OCR | `/v1/images/generations`, `/v1/images/edits`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/videos`, `/v1/ocr`, `/api/v2/ocr/jobs`, `/v1/native/dashscope/images/generations`, `/v1/native/dashscope/ocr/recognitions`, `/v1/native/bailian/audio/speech`, `/v1/native/elevenlabs/audio/speech`, `/v1/native/gemini/images/models/*` | `managed_key.go` POST `switch`，以及 `HasPrefix(path, "/v1/native/gemini/images/models/")` 规则 |

Responses 虽然已注册，仍受限：`router/managed_native.go` 在 `responses.create` 时强制把 `store` 置为 `false`，并拒绝 `previous_response_id`、`conversation`、`cachedContent`、`container` 及 provider-owned 文件/向量库引用；有状态的续接、后台任务与 Gemini 文件引用在独立作用域的历史所有者出现前不可用。这与上文「Stored Responses continuations … are refused until a separately scoped native history owner exists」一致。

### 白名单外返回 501 的代表性操作

以下路径不在 `managed_key.go` 的映射分支里，统一 `uh_key_` Key 访问时会命中 `target == ""` 的 501 分支；它们返回的是上面那个 `unified_operation_unsupported` 错误码，而不是进入原生鉴权、本地配额或 HTTP 转发回退。

| 代表性路径 | 当前返回 | 被排除的原因（代码 / 文档依据） |
| --- | --- | --- |
| `/v1/completions`（旧版 Completions） | 501 `unified_operation_unsupported` | 不是已注册的平台操作；适配器只接受标准 OpenAI Chat（channel type 1）与已声明 wire 的操作，替代协议/自动格式转换的计费语义尚未实现。见 `docs/unihub-platform-adapter.md` 「alternate protocols/resources, automatic Responses conversion … are rejected until their billing semantics are explicitly implemented」。 |
| `/v1/audio/translations`、`/v1/images/variations`（多模态变体） | 501 `unified_operation_unsupported` | 白名单只列了 `audio/transcriptions`/audio speech 与 `images/generations`/`images/edits`；多模态附加费与这些变体操作的计费语义未实现，文档明确拒绝「Multimodal/built-in tool surcharges」。 |
| `/v1/moderations` | 501 `unified_operation_unsupported` | 无对应平台注册操作；属于未在白名单内的内建/辅助能力，没有可靠计费映射。见 `managed_key.go` 的显式 POST `switch`（无 default 兜底）。 |
| `/v1/realtime`、`/v1/tools/search`、`/v1/tools/fetch`（替代协议 / 内置工具） | 501 `unified_operation_unsupported` | 属于「alternate protocols/resources」与「built-in tool surcharges」；文档明确拒绝 passthrough bodies、channel overrides、injected system prompts，且替代协议/内置工具计费语义尚未建立。 |

### 明确仍属未验证 / 未实现

- 有状态 Responses 续接（`store=true`、`previous_response_id`、`conversation`、`cachedContent`、`container`）、provider-owned 文件 / vector-store / container 引用、Gemini 文件引用、后台 Responses 任务：`managed_native.go` 明确拒绝，文档写明需独立作用域的历史所有者。
- 多模态 / 内置工具附加费、替代协议与资源、自动 Responses 转换、passthrough bodies、channel 参数/头覆盖、注入 system prompt：文档写明「rejected until their billing semantics are explicitly implemented」。
- 上表 501 路径及其它在 `managed_key.go` 中没有显式映射的路径：当前实现返回 501，不视为已支持、也不视为已验证计费。

本节的表格与结论均以 `router/managed_key.go`、`router/managed_native.go` 和本文档现有措辞为准；`router/managed_key_whitelist_test.go` 以契约测试钉住「显式集合 + 代表性 501 错误码 + 代表性白名单路径不被该 501 分支拒绝」这三点。

`GET /platform/v1/calls/{callID}` accepts a model credential and `X-UniHub-Task-ID`. It verifies the original model and exact actor/account/origin/task; a newly issued credential for that same scope may read the receipt. Response: `{call_id,status,charged?}`. It never returns prompts or credentials and never sends a provider request. `charged` is present only when supplied by the confirmed UniHub terminal response. Errors use ordinary OpenAI envelopes; duplicate logical calls return HTTP 409 and never replay upstream.

## Persistence and recovery

GORM tables `platform_model_calls` and `platform_model_attempts` use portable columns and query methods. The call stores verified immutable payer scope, model, body+task digest, published rate, held bound and terminal command intent. Model credentials are never stored. A call row is inserted before reserve; dispatch requires an atomic `RESERVED → UNKNOWN` fence and persists its attempt before upstream.

Successful provider responses with confirmed raw usage yield `SETTLE_PENDING`, then `SETTLED`. Definite failed HTTP responses with no output can retry within the original request; terminal pure failure yields `RELEASE_PENDING`, then `RELEASED`. Transport ambiguity, partial streams without final usage, missing usage and invalid/estimated usage remain `UNKNOWN`; they are never automatically refunded or replayed. Confirmed usage can settle even if a later stream transport failure occurred. At the original JSON/SSE boundary, prompt_tokens, completion_tokens and total_tokens must each exist as non-null canonical nonnegative integers within the supported integer range, with a consistent sum. Invalid/missing final usage clears earlier provisional usage; estimated or zero-default DTOs are never the provider observation. Missing/null optional token details represent no separately reported breakdown; explicit core zero remains a confirmed zero. Input subtracts cache-read and cache-write; output subtracts reasoning, producing mutually exclusive integer-string counters. A missing independent provider-cost observation remains empty/unknown, never fabricated as zero. UniHub independently calculates the charge and records overshoot under its platform-difference rules.

The final intent is persisted before sending settle/release. A bounded worker retries exactly that intent and never calls providers. Lost reserve responses and expired undispatched `RESERVING/RESERVED` leases are reconciled through GET of the original UniHub call. Its actor/account/origin/digest/rate/model must all match before release. Expired leases are fenced before recovery, so a paused original handler cannot resume dispatch after its reservation is released. Dispatched `UNKNOWN` calls retain their hold for investigation. Recovery orders attempts fairly by update time and processes at most 100 calls per pass; cancellation stops the worker before database shutdown.

Every legacy billing entry used by the relay rejects/skips platform contexts before local token/wallet/subscription debit, free-model bypass, violation fees and local usage counters. The first route never attaches a legacy `BillingSession`.

## Local verification

```
go test ./internal/platformbilling ./router ./controller ./service ./relay/channel/openai ./relay/common ./relay -count=1
go test -race ./internal/platformbilling ./router -run 'TestPlatform|TestDurable|TestUnknown|TestReceipt|TestRecovery|TestInvalid|TestExpired|TestLost' -count=1
go test -race ./dto ./internal/platformbilling ./router ./relay/channel/openai ./relay/helper -count=1 -timeout 120s
```

The opt-in real-UniHub harness uses an isolated temporary SQLite database and synthetic OpenAI upstream, and reads only one explicitly supplied environment file. No production provider store or gateway database is opened:

```
UNIHUB_PLATFORM_E2E=1 UNIHUB_PLATFORM_E2E_ENV_FILE=/absolute/path/to/local/dev.env \
  go test ./router -run '^TestPlatformLocalLiveFixture$' -count=1 -v -timeout 16m
```

It listens on `127.0.0.1:3001`, uses UniHub at `http://localhost:8088`, model `unihub-synthetic-model`, and immutable rate `local-e2e-chat-v1`. The environment file must contain `MODEL_SERVICE_TOKEN`. Its synthetic SSE emits content, an explicit `finish_reason: stop` frame, final usage and `[DONE]`. Diagnostic logs contain only request byte counts, HTTP status and an allowlisted error code, never credentials or message content. It stops on signal or after 15 minutes. Native production model execution and MySQL/PostgreSQL gateway migration execution are separate acceptance steps; the financial service integration can be exercised locally without real upstream keys.

## Account-key OCR text documents

`GET /v1/ocr/{callID}/document` and its platform alias
`GET /platform/v1/ocr/jobs/{callID}/document` return a bounded text document for
an existing completed OCR call. They require the original durable `uh_key_`
credential and freshly verify its actor, account, product, instance, model scope
and immutable key ID. Receipt-only `uh_read_` and task `uh_model_` credentials
cannot use this endpoint. Query-string credentials are refused. Authorization
is checked again after the stored document is read and projected.

The response is `{id,pages:[{index?,width?,height?,text,layout:[]}]}`. Layout
blocks contain only `label`, `content` and optional `bbox`. The stored result
must be complete, centrally settled, unrevoked, unexpired and match its recorded
size and SHA-256. Resource metadata is checked again after reading. At most
1000 pages and 16 MiB of projected JSON are returned. Responses use `no-store`.
This endpoint makes no supplier requests and issues no resource download grants.

Text projection removes image references, HTML tags, reference definitions,
hyperlinks and resource URLs from text and block labels/content. Tables,
formulas and available coordinates remain in the text document. Image bytes,
resource maps, raw provider fields and native payloads are excluded. Existing
SuperWagie `/result`, native status and bearer resource-download restrictions
remain unchanged. A settled receipt alone cannot release those resources.

The new route does not change OCR admission, reservation, rates, settlement or
retry policy. Submission still uses the persisted original idempotency identity;
an unknown result never authorizes a second supplier dispatch.

## Product credit video certificates

Set `UNIHUB_PRODUCT_CREDIT_VIDEO_POLICIES` to configure a product credit only
certificate for `videos.create` and `wan2.6-t2v`. The map uses the video
certificate schema, with `version` set to `video_v2`, `usage_policy` set to
`ali_wan26_t2v_cny6_v2`, and empty or omitted `rate_ids`. The certificate
binds the exact channel and producer. Product credit dispatch checks the
current and reserved certificates. Product credit pricing comes from
`ProductCreditPricing`.

`UNIHUB_VIDEO_POLICIES` remains the configuration for legacy video billing.
When no product credit certificate is configured, a valid `video_v2` legacy
certificate can also serve the product credit operation. Existing in-flight
and completed requests keep the video certificate and price recorded at
admission.

## Governed knowledge routing group

`UNIHUB_KNOWLEDGE_ROUTING_GROUP` optionally selects one existing ability group
for the pinned `text-embedding-v4` and `qwen3-rerank` producers. It is part
of the configuration digest reviewed by the `knowledge-bindings` operator.
The same certified routes must pass the channel, exact ability and key checks
in the selected group. The instance routing group still governs other models
and billing. If unset, the pinned producers also use the instance routing
group.
