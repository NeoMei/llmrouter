# Native metering coverage and migration

Native prices are explicit `ModelNativePricing[model][operation]` opt-ins. The model settings editor saves only on an explicit Save action. Local prices use currency base units; platform asset prices and receipts remain authoritative in UniHub. Existing ModelPrice/ModelRatio retain legacy meanings.

## Capability matrix (local implementation evidence, not production acceptance)

| Operation / adapter | Native unit / policy | Current verification boundary |
| --- | --- | --- |
| Chat, Responses, embedding | token / provider_reported_v1 | Raw usage presence and zero are tracked; adapter-specific tests determine support. Missing/conflicting usage stays pending. |
| Speech, MiniMax | character / provider_reported_v1 | Provider-reported character observations; reservation bounds require explicit admission support. |
| Speech, declared final-text adapters | character / unicode_scalar_v1 | Final sent Unicode scalars, including whitespace and combining marks. Never infer capability from a model name. |
| Transcription, supported provider receipts | audio_second / provider_reported_v1 | Exact scaled seconds; untrusted duration or unavailable evidence stays pending. |
| Image generation/edit | image / successful_outputs_v1 | Completed outputs only; partial/unknown results pending. Remote image dimensions remain unverified where no trusted output metadata or probe exists. |
| OCR | request / success_request | One successful terminal request; page pricing disabled. |
| Sora | video_second / sora_completed_seconds_v1 | Provider completion contract must be verified per adapter/model. |
| Kling | provider_unit / kling_final_units_v1 | Namespace required; decimal final units preserved. |
| Doubao video | provider_unit / doubao_completion_tokens_v1 | Explicit namespace; completion-token semantics must be certified per adapter. |
| Ali video | video_second / ali_video_duration_v1 or ali_wan26_t2v_billable_duration_v1 | Actual output/billable duration contract, never request echo. |
| Veo | video_second / veo_output_probe_v1 | Output probe remains unverified; do not claim production acceptance. |

This is a protocol capability inventory, not a blanket enabled-model list. All provider calls and live prices remain unverified. Local automated tests, isolated UniHub HTTP/PostgreSQL/ledger acceptance, and actual supplier acceptance are separate evidence levels. Async platform funding integration remains subject to the final integration report; this UI alone does not certify it.

## Offline migration

Run `bun scripts/native-metering-migration.ts scripts/native-metering-example.json` from the repository root with the frontend dependencies available. Examples are synthetic, not sale prices: 1500 characters at 1/1000 produce 3/2 currency units versus the supplied fixed request price 2; 1.001 seconds at 2 produce 1001/500 versus fixed 1. The output is always `enabled:false, write_options:false`; it never writes options, calls providers, or enables proposals. Supply actual reviewed legacy prices and explicit sample quantities before choosing a real migration.

The advanced catalog editor supports multiple token dimensions and image tier terms. Each image tier key must match the adapter's trusted output classification. An editor-valid tariff does not certify an upstream adapter or admission limit. OCR page units are rejected.

User receipts omit provider request identifiers, administrator reasons and raw observations. Pending calls show a reservation without a final charge; settled zero remains a visible zero. Admin reconciliation uses a unique action ID and reason, and distinguishes actual measurement, confirmed no output, and operator loss release. Platform releases remain UniHub-owned.
