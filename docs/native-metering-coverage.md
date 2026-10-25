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
