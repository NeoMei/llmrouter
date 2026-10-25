# UniHub platform Chat adapter

The adapter is opt-in. Existing `/v1` authentication, providers and local billing remain unchanged. Enable `UNIHUB_PLATFORM_ENABLED=true` with the explicit service origin, separate service token, enabled routing principal/group and limits shown in `.env.example`. No local wallet or token funds platform calls. The enabled principal must already belong to the fixed configured group. All gateway instances for the same UniHub service must share the same gateway database; the logical-call dispatch fence lives there.

## HTTP contract

`POST /platform/v1/chat/completions` accepts an opaque model credential in `Authorization: Bearer uh_model_...`, a stable `Idempotency-Key` for **each logical model request**, and `X-UniHub-Task-ID`. A subsequent tool loop uses a new call ID; provider retries within the original HTTP call reuse the same logical call with distinct persisted attempts. Both IDs allow 1–128 ASCII alphanumeric / `_ . : -` characters, beginning with an alphanumeric character. The gateway returns `X-UniHub-Call-ID` once admitted.

Platform requests reject duplicate JSON keys at every nesting level and require exact canonical DTO field names on protocol-owned objects, including nested messages, stream options and tools. Unknown or case-alias fields are rejected before reservation. User-defined keys inside function parameter schemas and metadata retain their case-sensitive meanings.

Function tools support the canonical optional `function.strict` boolean. Explicit `false` and `true` survive the shared OpenAI DTO and upstream serialization; absence stays absent. Platform requests reject null, other types, aliases and duplicate `strict` keys before financial reservation. This field is used by the actual OpenCode 2.1.3 SDK, so it is covered by a real router-to-provider regression as well as DTO round-trip tests.

Provide a published model and explicit positive `max_tokens` or `max_completion_tokens` within the configured output limit. The adapter bounds body bytes by both `UNIHUB_MAX_BODY_BYTES` and half `UNIHUB_MAX_INPUT_TOKENS`, conservatively reserving the full configured input limit plus output maximum. It uses the highest published rational input/cache price and output/reasoning price, sums exactly and rounds upward once. This is a hold, not an estimated final charge. `UNIHUB_MODEL_RATE_IDS` optionally pins each model to a published immutable rate ID; an ambiguous unpinned published rate fails closed. There is no invented production tariff or client-specified hold amount.

The initial supported provider is standard OpenAI Chat (channel type 1), JSON or SSE, with text messages, text-only content arrays and function tools. Streaming requests force `stream_options.include_usage=true`. Multimodal/built-in tool surcharges, alternate protocols/resources, automatic Responses conversion, passthrough bodies, channel parameter/header overrides and injected system prompts are rejected until their billing semantics are explicitly implemented. Server-side channel model mapping and ordinary existing channel selection/retry remain available. Browser headers/body cannot choose the payer or routing principal/group.

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
