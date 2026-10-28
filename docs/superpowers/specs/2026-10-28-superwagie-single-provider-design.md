# SuperWagie single LLMRouter provider design

## Approval and delivery boundary

The maintainer approved the following requirement on 2026-10-28: one LLMRouter provider must support access to every model operation required by SuperWagie's main Agent, built-in skills, and knowledge library. This includes auxiliary and child Agent calls, background consumers, and models invoked by bundled scripts or external skill runtimes.

This document records that approved scope and the proposed implementation contracts. It does not record implementation, deployment, supplier availability, or successful acceptance. Development may proceed in stages; full-provider completion requires every required consumer row to pass. A missing adapter, a `planned` label, or an unavailable runtime remains an open delivery item.

Implementation plan: [single provider plan](../plans/2026-10-28-superwagie-single-provider.md).

## Product boundaries

- UniHub owns identity, accounts, team membership, payer authority, model authorization, published sale rates, wallets, budgets, and final financial settlement.
- LLMRouter owns upstream credentials, executable model capabilities, protocol adaptation, channel selection, bounded request admission, supplier observations, and durable gateway call records.
- SuperWagie owns model-purpose preferences, Agent and skill execution context, knowledge-library settings, local runtime behavior, and product-facing configuration.
- A single provider contains multiple models and operations. The configured upstream model must actually support the selected operation and capability; compatibility must not be inferred from its name.
- Existing custom providers and local inference remain explicit user choices. Managed failures must not silently select a different payer, supplier, or local secret.

## Required coverage

| Requirement | Consumers and required behavior |
| --- | --- |
| R1 | Main Agent ordinary and streaming dialogue, complete tool loops, reasoning, structured output, and image/document inputs where the selected model supports them. |
| R2 | Auxiliary generation, summaries, child Agents, resumed sessions, and context compaction retain server-confirmed execution ancestry and independent logical calls. |
| R3 | Every built-in skill receives its Agent model and every direct model dependency through this provider, including subprocesses and external runtime bridges. |
| R4 | Knowledge ingestion and retrieval cover OCR, vision, text/multimodal embedding, rerank, and audio/video transcription dependencies. |
| R5 | Skill/product media operations cover short/file/continuous transcription, TTS, image generation/editing, and video generation, including asynchronous results. |
| R6 | One managed connection and one coherent model settings system cover all consumers. BYOK and local overrides are explicit and reusable. |
| R7 | Every paid logical call binds actor, payer, product, task, model, operation, immutable rate, and recoverable receipt. No second native charge is allowed. |
| R8 | Existing installs, saved preferences, active tasks, and existing knowledge indexes have a tested migration path. |
| R9 | Consumer output, authorization, usage, settlement, failure recovery, and installed-app behavior have separate evidence. |

Continuous dictation currently uploads buffered WAV segments. Buffered transcription and native bidirectional realtime are distinct transports and must be labeled accurately. Any selected model or consumer requiring native realtime needs its real session adapter and usage contract before its coverage row can pass.

The required skill inventory begins with the current model catalog's 13 skills: `superppt`, `ai-image-to-ppt`, `image-to-editable-pptx`, `supervideo`, `superwriter`, `WPSComposer`, `superweb`, `obsidian-excalidraw`, `grilling`, `grill-me`, `grill-with-docs`, `to-spec`, and `domain-modeling`. Inventory reconciliation must include all actually bundled/installed first-party skills and runtime bridges; the static list is not a completeness test.

## Source baseline and known gaps

Source was inspected on 2026-10-28. Both application repositories contain unrelated work in progress. Re-read the owning symbols before implementation and do not overwrite concurrent changes.

LLMRouter already has native capability metadata, media limits, embedding-space checks, multi-operation platform admission, durable receipts, and managed-key dispatch. `router/managed_key.go` currently returns only basic model-list fields. `internal/platformbilling/operation.go` restricts Responses input to a string and rejects tools. The managed route allowlist does not yet cover all native protocols or rerank. These are implementation gaps, not proposed scope exclusions.

SuperWagie's managed runtime currently projects a static text-only catalog, accepts primary Chat SSE, binds one model to a task, and serializes calls at task/session level. Its separate model-configuration subsystem already has connection and purpose bindings. Some actual consumers still require supplier-specific protocols, legacy keys, or direct script calls. Images' Gemini vision QA and existing skill-install updates require explicit migration.

The ongoing LLMRouter `product_funding` work serves another product-account migration. It is not evidence that cross-product SuperWagie credentials, origin attribution, or every model operation are complete. Reuse it only after proving the same authorization, metering, idempotency, and recovery contracts.

## Connection and capability contract

Normal users log in to UniHub and select an authorized payer. SuperWagie creates one managed connection with a stable internal identity. Preserve compatibility with persisted `unihub` identifiers; display branding can identify the service as LLMRouter.

Advanced users can explicitly create an API-key or self-hosted connection. A task grant must never be saved as a durable provider key, and the UniHub login access token must never be sent to an upstream supplier.

The versioned catalog contains:

- A stable public model ID and display name.
- The existing capability operation IDs from `relay/capability/operation.go`, protocol, approved same-origin endpoint path, and response transport.
- Actual tools, strict schema, reasoning, streaming, accepted input/output modalities, and provider parameter variants.
- Context/output limits, media formats and sizes, duration/count limits, and embedding-space identity.
- Account-scoped entitlement, operation availability, disabled reason, immutable rate reference, asset, unit, and rational price terms.
- Catalog version and freshness. Failed refresh retains the last valid catalog with an error/stale state; it must not become authoritative empty success.

Reuse native validated metadata and UniHub's account-scoped authorization/rate reads. Catalog discovery must not mint a broad execution grant, accept client-selected payer/group authority, or reveal another account's models or prices. API-key discovery remains restricted to that key's model scope. Account-bound discovery uses the authenticated backend context, independently of narrow per-call execution grants.

The proposed catalog routes are additive: LLMRouter extends managed `GET /v1/models` with capability metadata while retaining the standard list shape; a read-only internal catalog export supplies configured product/instance technical metadata to UniHub; UniHub exposes `GET /v1/accounts/{account}/model-catalog?product_id=superwagie`; SuperWagie exposes authenticated `GET /api/platform-model/catalog` using its selected authorized account. Internal export authentication is service-only and product/instance-scoped; it never grants execution or financial authority. The client cannot choose internal routing groups. UniHub joins the export with current account policy and immutable rates, and LLMRouter rechecks execution authority at admission.

Execution is allowed only when the consumer, gateway adapter, account policy, published operation tariff, and certified usage measurement all support the requested operation. A listed model alone is insufficient.

## Model resolution and execution

The shared runtime resolves `{connection, purpose, model, operation, parameters}` before admission. Protocol comes from validated model metadata. Ordinary settings do not ask users to choose Chat, Responses, Messages, or supplier-specific paths.

The task freezes actor, billing account, product, instance, root session or background job, and authorization provenance. Each call freezes its own model, purpose, capability operation, protocol, effective request digest, immutable rate, and output transport. Calls may use different models under the same task and run concurrently when the consumer permits it.

Child Agents, auxiliary calls, compaction, skills, and background knowledge jobs receive execution handles created by the backend. Each real model operation has a separate logical call ID. `parentCallId` is retained when a model/tool call caused another operation; independent user-triggered operations have their own root task. Client-supplied task/account strings do not create authority.

Grant caching must include task, model, operation, identity/account, and authorization revision as relevant to the credential contract. Expiry or revocation must not change an admitted call's payer or prevent authorized receipt reconciliation. Existing task and call records need a backward-compatible migration.

A CLI/Python or external runtime receives a private operation invocation endpoint or a narrowly scoped, short-lived execution capability. It never receives a provider-wide key or UniHub login token. Broker requests use a private pipe or authenticated local transport; external/hosted/mobile execution needs an explicitly authenticated equivalent. Secrets must not appear in CLI output, evidence files, or persistent model settings.

## Protocol and accounting rules

- Reuse complete relay implementations behind managed admission. Extend validation and usage collection together for Chat, Responses, Messages, Gemini, media inputs, and every required non-chat operation.
- Wire operation and billing operation are separate concepts. Native text protocols may share an existing token tariff only when their confirmed usage dimensions have identical documented semantics. Built-in tool surcharges or new units require their own published terms.
- Extend metering for required missing operations such as rerank in both LLMRouter and UniHub. Choose the unit from actual provider observations; do not invent prices or reinterpret another operation's rate.
- Enforce bounded body size, quantities, duration, dimensions, token limits, and reservation ceilings before dispatch. Use the existing quota helpers and audit saturation where native quota conversions apply.
- A logical retry reuses its call ID and canonical request digest. Definitive upstream failures may have separate persisted attempts within that logical call. Ambiguous transport or output must not trigger a new paid call.
- JSON, SSE, binary audio, image output, WebSocket sessions, and asynchronous jobs have separate completion conditions. `[DONE]` is not a general completion test.
- Async submit, status, result/resource retrieval, cancellation where supported, timeout, and restart recovery remain within the same actor/payer/task scope. Readback never redispatches a supplier request.
- Missing/conflicting usage remains unresolved for reconciliation. Settlement and release replay idempotently; unknown outcomes are not automatically refunded.

## Knowledge-library migration

Persist a semantic `embedding_space` identity containing provider/model/version, dimensions, and input semantics. Indexing and query embedding use the same identity. Equal vector dimensions alone do not prove equal spaces.

Existing installations currently guard model name, endpoint, and dimensions. Migration may preserve an index across a verified transport or credential change only when it proves the same semantic space. If the old space cannot be established, require explicit reindexing. An account-wide recommended model update must not silently replace an existing library's embedding model.

OCR must preserve its actual multipart, polling, result-download, and parser contract. ASR must cover knowledge audio/video ingestion as well as microphone input. Rerank must be consumed by actual search, beyond a connection probe.

## Settings and installed-skill migration

Use the existing model settings' connection and purpose structure. Show account and payer context with a user-center link. Keep account, wallet, role, and budget authority in UniHub.

Voice settings retain recording/playback behavior, hotkeys, voice parameters, and local/system choices. Knowledge settings retain library state, parser/index options, and rebuild actions. Cloud model credentials and model-purpose selection live in model settings.

Migrate explicit legacy BYOK settings to reusable connections with origin information and conflict handling. Make migration repeatable; clear an old secret only after the new configuration is durably saved. Managed operation failure must not fall back to legacy environment variables or secret files.

Upgrade both bundled and already-installed managed skill helpers/engines. Respect user edits using the existing managed-file baseline/identity mechanism. Verify Python helpers, Gemini vision QA, SuperPPT/SuperVideo runtime copies, external editable-PPT runtime bridges, and packaged resolver locations. A bundled-source patch alone does not prove installed consumers changed.

## Completion evidence

Maintain a consumer-to-operation acceptance ledger with source/runtime version, model, protocol, fixture result, actual account-authorized result, usage, settlement receipt, artifact/output verification, and environment. Secrets and sensitive prompts are excluded.

For each required dependency, prove a real product consumer uses the one connection without a supplier-specific key. Exercise cross-consumer concurrency, payer changes, revocation, insufficient funds, duplicate calls, partial streams, async restart, catalog failure, and old-install upgrade.

Run deterministic protocol and isolated accounting tests first. Actual paid supplier calls, deployments, releases, payment collection, and public publication remain separately authorized actions. Never treat fixture tests as actual supplier or installed-app acceptance.

Full completion requires R1 through R9 and every required consumer row to pass. Partial milestones may be reported only with their explicit remaining rows.
