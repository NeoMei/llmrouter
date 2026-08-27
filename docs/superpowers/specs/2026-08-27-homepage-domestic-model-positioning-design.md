# Homepage Domestic Model Positioning Design

## Goal

Rewrite the default LLMRouter homepage so it presents the service as a unified access layer for domestic AI models, matching the models actually enabled on `llmrouter.quukk.com` and the user journey of obtaining an API key, choosing a model, and making requests.

## Evidence and positioning

The production model marketplace currently exposes 23 enabled models. The visible catalog is entirely composed of domestic model families: DeepSeek, Zhipu GLM, Moonshot Kimi, Alibaba Qwen, ByteDance Doubao, and MiniMax.

The current homepage instead foregrounds OpenAI, Claude, Gemini, and Llama; describes global deployment; demonstrates foreign-provider protocol tabs; and tells visitors to add upstream keys or deploy their own gateway. Those messages describe the upstream project or an administrator workflow rather than this hosted service.

The approved positioning is therefore:

- Primary promise: one API provides access to leading domestic AI models.
- Model proof: name the domestic families that are actually enabled.
- User journey: get an API key, choose a domestic model, call the unified API, and monitor usage.
- Protocol and coding-client compatibility remain supporting capabilities, not the primary model story.

## User-visible changes

### Hero

- Replace the infrastructure-oriented badge with a domestic-model access label.
- Replace the generic “vast range of AI models” headline with “One API for leading domestic AI models.”
- Name DeepSeek, GLM, Kimi, Qwen, Doubao, and MiniMax in the supporting description.
- Preserve the authenticated `Get API Key` action, unauthenticated sign-up action, `/pricing` action, local `/docs` action, and the approved Claude Code, Codex, and OpenCode links.
- Preserve the previously approved LLM API multi-protocol support sentence.

### API demonstration

- Replace the Chat, Responses, Claude, and Gemini tabs with DeepSeek, GLM, Kimi, and Qwen examples.
- Use model identifiers that are present in the production catalog and send all four examples through the unified `/v1/chat/completions` route.
- Keep the demo illustrative: it must not expose credentials or imply live benchmark values.

### Service facts

- Replace upstream-project capacity counters with conservative facts supported by the current production catalog and homepage integrations:
  - `20+` domestic models available.
  - `6+` domestic model brands.
  - `3` supported coding clients.
  - `1` unified API key.
- Use thresholds rather than the exact current model count so normal catalog growth does not immediately stale the page.

### Feature section

- Replace foreign-model pills with DeepSeek, GLM, Kimi, Qwen, Doubao, and MiniMax.
- Replace “Global Coverage” with unified routing for domestic model services.
- Keep security, performance, billing, and protocol compatibility as supporting benefits.
- Replace the self-hosting-oriented open-source feature card with a hosted-service benefit such as usage visibility or coding-tool readiness.
- Retitle the section around domestic AI applications and production workflows.

### Getting started and final CTA

- Replace the administrator flow of adding upstream keys and channels with the customer flow:
  1. Create an API key.
  2. Choose an enabled domestic model.
  3. Call the unified API and monitor usage and cost.
- Replace “deploy your own gateway” language with a direct invitation to use domestic models through the hosted service.
- Keep the registration action and link the secondary action to the existing model marketplace at `/pricing`, labelled as viewing models.

## Information that remains unchanged

- Claude Code, Codex, and OpenCode are client integrations, not claims about the supplied model inventory; their approved links remain unchanged.
- The service can continue to describe multi-protocol compatibility, but it must not present foreign model vendors as the primary catalog.
- Protected new-api and QuantumNous attribution, copyright headers, package metadata, and project references must not be changed or removed.
- The shared footer attribution remains intact.

## Implementation design

Create a focused `home-content.ts` module for the featured domestic model list, service facts, and terminal demo data. Existing homepage components consume this module so the production-facing model contract is centralized and directly testable without rendering internals.

Keep marketing copy in `t(...)` calls and add every new key to all seven supported locales through the sanctioned i18n script. Do not edit locale JSON files manually.

The visual structure and responsive layout remain unchanged. This is a positioning and content correction, not a homepage redesign.

## Test strategy

Follow red-green TDD:

1. Extend the focused homepage copy test so it first fails against the current foreign-model positioning.
2. Add pure-data tests for the domestic model list, service facts, and four terminal demos.
3. Implement the centralized content contract and update components minimally.
4. Add translations through the i18n script, then run i18n synchronization.
5. Run focused homepage tests, typecheck, lint for changed files, format checks, the production build, and a browser-visible local homepage check.

## Out of scope

- Backend routing, channel configuration, pricing, or model availability changes.
- Dynamic production model discovery on the homepage.
- Changes to top navigation, documentation content, authentication, or the model marketplace.
- Publication to GitHub or production deployment unless separately requested.
