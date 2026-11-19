# Native metering coverage and migration

Native prices are explicit `ModelNativePricing[model][operation]` opt-ins. The model settings editor saves only on an explicit Save action. Local prices use currency base units; platform asset prices and receipts remain authoritative in UniHub. Existing ModelPrice/ModelRatio retain legacy meanings.

## Capability matrix (local implementation evidence, not production acceptance)

| Operation / adapter | Native unit / policy | Current verification boundary |
| --- | --- | --- |
| Chat, Responses, embedding | token / provider_reported_v1 | Raw usage presence and zero are tracked; adapter-specific tests determine support. Missing/conflicting usage stays pending. |
| Speech, MiniMax | character / provider_reported_v1 | Provider-reported character observations; an explicit per-model reservation cap is required. Over-cap evidence stays pending. |
| Speech, declared final-text adapters | character / unicode_scalar_v1 | Final sent Unicode scalars, including whitespace and combining marks. Never infer capability from a model name. |
| Transcription, supported provider receipts | audio_second / provider_reported_v1 | Exact scaled seconds; untrusted duration or unavailable evidence stays pending. |
| Transcription, bounded PCM WAV | audio_second / server_audio_measurement_v1 | Server-measured source is pinned; a supplier duration cannot overwrite it. Other codecs require a certified measurement adapter before admission. |
| Image generation/edit | image / successful_outputs_v1 | Completed outputs only; partial/unknown results pending. Remote image dimensions remain unverified where no trusted output metadata or probe exists. |
| OCR | request / success_request | One successful terminal request; page pricing disabled. |
| Sora | video_second / sora_completed_seconds_v1 | Provider completion contract must be verified per adapter/model. |
| Kling | provider_unit / kling_final_units_v1 | Namespace required; decimal final units preserved. |
| Doubao video | provider_unit / doubao_completion_tokens_v1 | Explicit namespace; completion-token semantics must be certified per adapter. |
| Ali video | video_second / ali_video_duration_v1 or ali_wan26_t2v_billable_duration_v1 | Actual output/billable duration contract, never request echo. |
| Veo | Disabled | Output probing is not implemented. Generic and Veo policies reject before reservation. |

This is a protocol capability inventory, not a blanket enabled-model list. No live provider calls or production price changes were made. The declared video policies support one output or an authoritative task-level supplier unit. Requests for unsupported plural outputs reject before dispatch. Local automated tests, isolated UniHub HTTP/PostgreSQL/ledger acceptance, and actual supplier acceptance are separate evidence levels. See [verification](native-metering-verification.md) for the final results.

## Offline migration

Run `bun scripts/native-metering-migration.ts scripts/native-metering-example.json` from the repository root with the frontend dependencies available. Examples are synthetic, not sale prices: 1500 characters at 1/1000 produce 3/2 currency units versus the supplied fixed request price 2; 1.001 seconds at 2 produce 1001/500 versus fixed 1. The output is always `enabled:false, write_options:false`; it never writes options, calls providers, or enables proposals. Supply actual reviewed legacy prices and explicit sample quantities before choosing a real migration.

The advanced catalog editor supports multiple token dimensions and image tier terms. Each image tier key must match the adapter's trusted output classification. An editor-valid tariff does not certify an upstream adapter or admission limit. OCR page units are rejected.

User receipts omit provider request identifiers, administrator reasons and raw observations. Pending calls show a reservation without a final charge; settled zero remains a visible zero. Admin reconciliation uses a unique action ID and reason, and distinguishes actual measurement, confirmed no output, and operator loss release. Platform releases remain UniHub-owned.

## Reservation and recovery

`ModelNativeReservationLimits` is a separate local setting: public model → `speech` or `video` → `{ "value": "positive integer", "scale": 0 }`, bounded to 2147483647. It is a reservation ceiling, never final usage. Supplier-reported speech requires the setting; provider-unit video prefers it and retains the existing `LLMROUTER_NATIVE_VIDEO_PROVIDER_CAPS` environment fallback. No ceilings or sale prices are guessed during migration. The editor saves prices and limits independently.

Platform operation rates come from UniHub. `UNIHUB_OPERATION_RATE_IDS` pins the operation/asset/model rate; `UNIHUB_OPERATION_LIMITS` declares model request bounds and measurement policy. Supplier-reported speech requires an explicit `max_quantity`; provider-unit video requires `provider_unit_cap`, matching `provider_unit`, and the pinned policy. A native tariff is never a reason to debit the local platform principal.

The journal freezes the price, unit, funding source and reservation at admission. A local reservation that has not dispatched expires after a 60-second dispatch lease; dispatch and release use the same database fence. Once sending may have occurred, unknown outcomes remain held for reconciliation and are never automatically retried. Confirmed evidence, final settlement intents, and receipt projection replay idempotently after restart. Typed OCR refusals and provider-confirmed empty-output video failures release once. Provider task binding and persisted video task insertion are atomic; OCR recovers bindings from its durable job record.

## Product wallet settlement

The verified `product_v1` lane uses 100 credits per CNY. A credit contains 1,000,000 microcredits (`CREDIT/6`). Reservations and final charges use decimal signed 64-bit ledger amounts. Local quota and the existing native cash and credit conversions retain their original limits and units.

Product prices freeze the canonical public model, operation, tariff revision and exchange rate at admission. The tariff uses the highest configured comparable supplier rate for each billing dimension, after currency normalization. The administrator can adjust the default multiplier of 1.5 in the model pricing settings. If no supplier rate is configured for the model and operation, the published native rate applies. The observed supplier and usage are recorded separately from the public tariff.

For banded Chat prices, set `input_token_bands` on each supplier tariff with ascending inclusive `max_input_tokens` and a `prices` map for every declared term. Comparable suppliers must use the same boundaries. The total confirmed prompt tokens, including cache hits, select one band for the entire call. The hold covers the most expensive request reachable within the certified input and output bounds. The product catalog returns each effective band in `CREDIT/6` with its revision.

Banded Chat requires a product certificate in `UNIHUB_PRODUCT_CREDIT_TEXT_INPUT_POLICIES` for the canonical public model, exact OpenAI-compatible channel, upstream model and HTTPS Chat Completions URL. It uses the full-body-byte text policy and accepts text and tool calls. Explicit cache controls and media require a separate producer contract. A separately priced implicit cache counter must be confirmed by the supplier. If it is unavailable, the call waits for reconciliation.

Direct DeepSeek product Chat uses a channel type 43 certificate for `https://api.deepseek.com/v1/chat/completions` and one band with `input`, `cache_read`, `output`, and `reasoning` terms. Output and reasoning use the same rate. Supported upstream IDs are `deepseek-flash`, `deepseek-v4-flash`, `deepseek-v4-flash-vision-exp`, and `deepseek-v4-pro` for text input. Custom function tools remain available. The hold binds the exact request body and target. Settlement requires matching prompt total, cache hit and cache miss counts, and a terminal usage frame with `[DONE]` for streaming calls. A supplied cache-hit alias must agree. See the [DeepSeek Chat request and usage contract](https://api-docs.deepseek.com/api/create-chat-completion/) and [model price list](https://api-docs.deepseek.com/quick_start/pricing/).

Wan video and masked image edits retain their certified supplier task binding. Product account owners can recover those results with the original idempotency key and a fresh product authorization proof. Certified synchronous image generation, Bailian speech and DashScope transcription support `GET /platform/v1/calls/{original-idempotency-key}/result` after UniHub confirms settlement. Successful reads return the original validated output and its SHA-256 digest. Synchronous output stays encrypted in the Router journal for 24 hours and requires the same stable Router billing service key throughout that period. DashScope transcription accepts bounded inline PCM WAV.

Managed PaddleOCR accepts a local multipart PDF for the product wallet at `POST /platform/api/v2/ocr/jobs`. Its input limit is 20 MiB and 200 PDF pages. A repeated idempotency key with the same document and options resolves to the original call, even if the client rebuilds the MIME envelope. For a settled job, `GET /platform/api/v2/ocr/jobs/{original-idempotency-key}/document` returns normalized text and layout with a SHA-256 digest. The original product Key and a current product authorization proof are required. The private document remains recoverable within the existing OCR retention period.

A fixed-price OCR request, exact Unicode character pricing and certified Wan video with its exact billed duration can provide a provable minimum amount for the balance check. Other operations may have a bounded reservation without a proven minimum.
