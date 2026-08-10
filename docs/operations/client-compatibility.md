# Client Compatibility Operations Guide

This guide documents how to operate the Codex Gateway client-compatibility layer for Codex CLI and Claude Code.

## Public model routes and aliases

Public model routes are stored in `client_model_routes` and `client_model_targets`.

- A public model route has a unique `public_id` (e.g. `deepseek/deepseek-v4-flash`),
  an optional list of `aliases`, a client scope (`all`, `codex`, `claude`, or `gemini`),
  and one or more targets.
- Each target points to an enabled channel, an upstream model name, a billing model,
  and an ordered list of preferred wire protocols (`responses`, `openai_chat`,
  `anthropic`).
- The catalog rejects alias collisions at startup/validation time. If two routes
  share the same alias, the second one returns HTTP 409.

Management endpoints (admin only):

- `GET /api/client_model_routes`
- `POST /api/client_model_routes`
- `PUT /api/client_model_routes/:id`
- `DELETE /api/client_model_routes/:id`

## Wire protocol selection

The gateway resolves the requested model alias before price lookup and channel
selection. The selected wire is determined by:

1. The request path (e.g. `/v1/responses` implies `responses`,
   `/v1/messages` implies `anthropic`).
2. The route target's declared preferred wires.
3. Capability probes (when implemented) or explicit management configuration.

The resolved billing model, upstream model, route ID, and selected wire are stored
in the relay audit metadata.

## Stateful Responses continuity

The bounded `responsesstate` store replays `previous_response_id` when the
upstream wire does not support it natively. In production, enable Redis and set
`GATEWAY_AES_KEY` for encrypted multi-instance storage. The in-memory store is only
suitable for single-instance development.

## Client setup and restore

Use the `codex-gateway` CLI:

```bash
codex-gateway init
```

This writes managed blocks into `~/.codex/config.toml` (Codex) and `~/.zshrc` (Claude)
and stores a receipt under `~/.codex-gateway/receipts/`. Restoring is atomic:

```bash
codex-gateway # restore subcommand is implemented in client-state.ts
```

## Diagnostics

Run the built-in doctor:

```bash
codex-gateway doctor -s https://gateway.example -t sk-... -m deepseek/deepseek-v4-flash
```

The doctor checks configuration ownership, `/healthz`, authenticated `/v1/models`,
model visibility, a minimal native Responses request, a tool round trip, Anthropic
Messages, streaming termination, and receipt integrity. It exits nonzero on a
critical failure and never prints tokens or prompts.

## Route-decision explainability

For each request that resolves a public model route, the gateway records a bounded
trace keyed by request ID. Admins can retrieve it for up to five minutes:

```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://gateway.example/api/log/$REQUEST_ID/route-decision"
```

The trace contains the requested alias, public model, resolved model, selected
wire, route ID, and per-channel attempt outcomes. It never contains API keys,
`Authorization` headers, or prompt/response content.

## CORS

Set `TRUSTED_ORIGINS` to a comma-separated list of exact origins. In production,
`RUN_MODE=production` requires at least one HTTPS origin and rejects wildcard
origins. The gateway calls `middleware.ValidateCORSConfig()` at startup and refuses
to start if the configuration is insecure.

## Release preflight

`scripts/release-preflight.sh` validates required secrets, container digests,
HTTPS object storage, secure cookies, and CORS configuration. It also checks that
stateful Responses continuity is backed by Redis when enabled.

## Acceptance matrix

Run `scripts/client-e2e.sh` against an isolated deployment before any production
traffic change:

```bash
GATEWAY_URL=https://gateway.example API_TOKEN=sk-... MODEL=deepseek/deepseek-v4-flash \
  bash scripts/client-e2e.sh
```

The script produces one JSON evidence file per check under `e2e-evidence/` and
verifies:

- Codex: responses/chat text, streaming, function tools, previous_response_id
- Claude Code: Anthropic Messages text and streaming
- Security: relay token rejected on management routes, untrusted CORS origin denied
- Routing: route-decision trace availability

## Rollback

To disable public model routing without deleting records, disable the route in the
admin UI. The legacy model name will be used for billing and upstream requests.
