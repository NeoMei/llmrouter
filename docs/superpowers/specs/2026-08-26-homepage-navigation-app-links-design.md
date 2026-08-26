# Homepage Navigation and Coding App Links Design

## Goal

Update the llmrouter homepage so authenticated users can reach API key management directly, the homepage documentation button always opens the site's own documentation, and the supported-app shortcuts highlight the coding agents the service is intended to support.

## Scope

The change is limited to the homepage Hero. It does not change the backend `docs_link` setting, top navigation behavior, system-setting forms, or unauthenticated primary call to action.

## User-visible behavior

- When the visitor is authenticated, the primary Hero button reads `Get API Key` through i18n and navigates internally to `/keys`.
- When the visitor is unauthenticated, the existing `Get Started` button and `/sign-up` destination remain unchanged.
- The Hero documentation button always navigates internally to `/docs`. It no longer reads `status.docs_link`, so a configured or default New API documentation URL cannot affect this homepage button.
- The supported-application row contains exactly three external links:
  - Claude Code: `https://claude.com/product/claude-code`
  - Codex: `https://openai.com/codex/`
  - OpenCode: `https://opencode.ai/`
- Cherry Studio, CC Switch, and the non-link `More Apps` pill are removed from the Hero.
- External application links open in a new tab with `noopener noreferrer`.

## Implementation design

Keep rendering in the existing Hero component and reuse the installed `@lobehub/icons` package for Claude Code, Codex, and OpenCode icons. Store the homepage destinations and supported-app metadata in a focused module so the routing and external-link contract can be tested without duplicating JSX or introducing network-dependent images.

Use TanStack Router `Link` components for `/keys` and `/docs`. Do not retain the Hero's external-documentation branch because the Hero documentation destination is now a fixed internal route.

## Error handling and resilience

The three application icons are bundled dependencies, so rendering does not depend on remote favicons or image-load fallback code. Internal links remain under the router, while external links retain explicit new-tab security attributes.

## Internationalization

Render the authenticated call-to-action through `t('Get API Key')`. Synchronize locale files with the existing i18n tooling if that key is not already present. Product names remain untranslated proper nouns.

## Verification

Follow test-driven development for the new homepage link contract:

1. Add a failing focused test that expects `/keys`, `/docs`, and the three approved application URLs and names.
2. Add the minimal production configuration and Hero changes needed to pass it.
3. Run the focused test, TypeScript typecheck, lint for affected files, formatting checks, and a production build.

No backend or database test is required because this design deliberately leaves backend configuration unchanged.
