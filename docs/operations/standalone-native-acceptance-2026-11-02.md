# Standalone native acceptance record — 2026-11-02

This record captures reproducible LLMRouter-side evidence for the standalone
deployment described in [the runbook](../standalone-llmrouter-acceptance.md).
It does not certify a supplier account, a deployed environment, a real
payment, or an installed SuperWagie runtime.

## Configuration contract

The standalone profile is explicit:

```dotenv
UNIHUB_PRODUCT_ENABLED=false
UNIHUB_PLATFORM_ENABLED=false
NATIVE_PUBLIC_ACCESS=true
```

An API key created without model limits is represented by
`model_limits_enabled=false`, so native authorization evaluates the enabled
channel and ability catalog instead of a per-key allow-list. Native quota and
usage remain local; UniHub can be enabled later for managed credentials and
central settlement.

## Fresh verification

All commands were run from the repository root on 2026-11-02.

| Contract | Command | Result |
| --- | --- | --- |
| Native/compatible HTTP routing, one key across chat, embedding and rerank, quota settlement and rejection cases | `go test ./router -run '^TestKnowledgeCompatibleHTTP$' -count=1 -v` | Passed; all subtests passed |
| Native audio producer and usage journal | `go test ./router -run '^TestAudioGatewayNativeProducerAndJournal$' -count=1 -v` | Passed |
| Native OCR and owned-resource routing | `go test ./router -run '^TestPaddleActualNativeRouteOwnedResources$|^TestQwenOCRNativeGeometryActualRoute$' -count=1 -v` | Passed |
| Managed chat/responses/Claude/Gemini wire contracts and catalog metadata | `go test ./router -run '^TestManagedNativeGenerationExactWire$' -count=1 -v` | Passed |
| Provider capability and public resource route manifests | `go test ./tests/contracts -count=1 -v` | Passed |
| Backend regression suite | `go test ./... -count=1` | Passed |
| Frontend source tests, typecheck and production build | `bun test src && bun run typecheck && bun run build` | 182 tests passed; build passed |

The HTTP fixtures also assert that a successful call records usage and settles
the local quota, while invalid credentials, model scope, malformed requests,
and exhausted/invalid usage paths are rejected before or after dispatch as
appropriate.

## Remaining acceptance gates

The repository still has no evidence for a live supplier/account call, a
production deployment readback, a real recharge callback, or an installed
SuperWagie runtime using the deployed URL and key. Those gates must be run
against the target environment before declaring the business requirement fully
accepted.
