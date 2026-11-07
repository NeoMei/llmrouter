# Bailian speech with an account API key

The same managed SuperWagie account key used for chat, images and embeddings can
call explicitly configured Bailian speech routes. The account model catalog
advertises `bailian.audio.speech.create`, protocol `bailian`, method `POST`,
endpoint `/v1/native/bailian/audio/speech`, and transport `binary`.

```json
{
  "model": "qwen3-tts-flash",
  "input": {
    "text": "你好，欢迎使用语音服务。",
    "voice": "Cherry",
    "language_type": "Chinese"
  }
}
```

Send `Authorization: Bearer <account key>`, `Content-Type: application/json`,
and an `Idempotency-Key` that identifies this synthesis. A successful response
is bounded, validated PCM WAV audio with `Content-Type: audio/wav` and
`X-UniHub-Call-ID`. The server fetches the supplier audio resource; the client
never receives a supplier credential or needs to fetch a supplier URL.

The existing producer supports `qwen3-tts-flash`, its `2025-11-27` snapshot,
`qwen3-tts-instruct-flash`, and its `2026-01-26` snapshot. Text is limited to
600 Unicode scalars. Admission also requires a configured reservation covering
twice the input scalar count; this is a conservative bound for the supplier's
Han-character billing rule, not final usage. The instruct
models additionally accept `instructions` and `optimize_instructions` inside
`input`; explicit `false` is preserved. OpenAI `speed` and `response_format`
fields are not part of this native request contract.

## Required operator configuration

- An enabled Ali channel with its native DashScope origin, one supplier key,
  matching enabled ability rows, and an enabled public route/target.
- Route metadata declaring `bailian.audio.speech.create` with text input and
  audio output. The target may use `preferred_wires: []`.
- `ModelNativePricing[billing_model].speech`: a `metered_v2` speech price using
  policy `provider_reported_v1` and exactly one `characters` / `character` term.
  The rational term is USD per supplier-reported character, consistent with
  LLMRouter's native quota conversion. Preserve pricing-source provenance in
  the operator's pricing manifest, including estimated rates.
- `ModelNativeReservationLimits[billing_model].speech`: an explicit integer
  quantity between 1 and 1,200 supplier billing units, for example
  `{"value":"1200","scale":0}`. The full 600-scalar input limit needs 1,200
  units; a smaller configured bound narrows admitted input length.

Existing `ModelPrice` per-request values are retained and never reinterpreted
as character prices. Missing native tariff, missing reservation bound,
unsupported channel origin/model, or channel overrides disable execution in
the account catalog. They must be corrected explicitly before testing.

## Billing and failure behavior

Admission freezes the configured character tariff, group multiplier and quota
conversion, and reserves the configured character bound with UniHub using the
original account key. [Bailian's billing rules](https://help.aliyun.com/zh/model-studio/model-pricing)
count a Han character as two billing units and other characters as one. The
admission bound uses two per scalar without attempting to replace the supplier's
Unicode classification or final usage. The server rechecks the exact target
identity, capabilities, supplier key and origin after the final remote account
key verification, immediately before the durable dispatch fence. It records supplier usage and
audio evidence without resource URLs or credentials. Final billing uses the
supplier's `usage.characters`, including explicit zero, rather than input
length. A concurrent price edit cannot change an already reserved call.

Audio is returned only after native settlement succeeds. Missing/invalid usage,
failed audio downloads, cancellation and uncertain supplier failures retain
`UNKNOWN` for reconciliation; they do not trigger a second synthesis or an
invented charge. A settlement outage retains `SETTLE_PENDING` and its durable
intent for existing billing recovery. The audio observation records delivery
as false at the pre-settlement observation boundary and is not a client
playback receipt. Reusing a completed synthesis idempotency key returns a
conflict without another supplier call. Local routing-user balances and quotas
are not debited.

## Regression checks

```sh
go test ./router -run 'TestManagedBailianSpeech|TestProviderCatalog|TestManagedNative' -count=1
go test ./service/modelcatalog ./relay/channel/nativespeech ./internal/platformbilling -count=1
```

The controlled tests exercise the real gateway, native producer, HTTP audio
download and local billing journal, with local supplier/UniHub fixtures.
Production acceptance additionally requires account catalog readback and a
short paid synthesis with the original key, verified WAV bytes and its final
native billing receipt.
