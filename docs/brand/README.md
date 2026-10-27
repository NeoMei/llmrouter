# LLMRouter / 万流 visual identity

The Chinese product name is **万流**. Use **万流 · LLMRouter** when both names
are shown together on product-facing surfaces.

After reviewing the functional graphic concepts on 2026-10-25, the user selected A — Confluence. The selected mark is **Confluence**: three model/account lanes converge into one unified outlet. Its flat geometry, warm terracotta and dark brown typography relate to the user-supplied Quukk identity while explaining LLMRouter's core job without a letter monogram.

## Artwork

- `web/public/brand/llmrouter-confluence.svg`: transparent 256 × 256 vector master; three lanes converge into one outlet. `llmrouter-mark.svg` remains an identical compatibility alias.
- `llmrouter-mark-mono.svg`: black monochrome master for template/presentation uses.
- `llmrouter-wordmark.svg`: horizontal mark and outlined Public Sans 650 lettering in `#302A25`.
- `llmrouter-wordmark-dark.svg`: warm-light mark `#F4C5A7` and ivory lettering `#F8F5F0`.
- `apple-touch-icon.png`: 180 × 180 ivory app tile.
- `web/public/logo.png`: transparent 512 × 512 compatibility asset.
- `web/public/favicon.ico`: 16/24/32/48/64/128/256px compatibility favicon.
- `electron/icon.png`: 1024 × 1024 app tile.
- Electron macOS tray templates: black RGBA artwork at 22/44px; Windows/Linux tray: terracotta at 32px. `electron/create-tray-icon.js` regenerates tray variants with the existing Electron runtime, never API lettering or a transparent placeholder.

The master has no external font/image dependencies. Wordmarks convert Public Sans glyphs to paths; its OFL is included alongside the assets. At 16px use the mark by itself; use the horizontal wordmark where there is enough room. Keep proportions and transparent padding. Do not substitute the text wordmark into square favicon slots.

## Website rollout

Default branding and initial HTML use `/brand/llmrouter-confluence.svg`, avoiding reuse of a previously cached upstream logo URL. The PNG/ICO paths also contain the new artwork for older clients. Existing administrator-supplied custom logos remain supported. Production may serve these exact static artifacts at Nginx and set the Logo option to this SVG, independently of the backend release. Asset publication must retain the current backend image, existing site routing and other settings, with rollback copies of the previous logo option and Nginx file.

Upstream New API/QuantumNous legal, licensing, copyright and About attribution remain in place. This identity change does not imply signed or verified desktop installers; those require the normal packaging/signing pipeline.
