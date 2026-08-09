import type { KnipConfig } from 'knip'

const config: KnipConfig = {
  // Static translation keys are scanned by scripts/sync-i18n.mjs rather than
  // imported at runtime, so they are an explicit tool entrypoint.
  entry: ['src/main.tsx', 'src/i18n/static-keys.ts'],
}

export default config
