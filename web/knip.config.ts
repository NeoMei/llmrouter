import type { KnipConfig } from 'knip'

const config: KnipConfig = {
  // Static translation keys are scanned by scripts/sync-i18n.mjs rather than
  // imported at runtime, so they are an explicit tool entrypoint.
  entry: [
    'src/main.tsx',
    'src/i18n/static-keys.ts',
    // Custom Playwright config/testMatch roots and the async Rsbuild browser entry.
    // Shared fixtures are reached through imports rather than blanket entry globs.
    'playwright.settings.config.ts',
    'playwright.wallet-source.config.ts',
    'playwright.technical.config.ts',
    'src/features/wallet/__tests__/wallet-source.spec.ts',
    'src/features/keys/__tests__/settings-state.spec.ts',
    'src/features/native-metering/__tests__/settings-state.spec.ts',
    'src/features/usage-logs/__tests__/settings-state.spec.ts',
    'src/features/usage-logs/__tests__/technical-usage.spec.ts',
    'src/features/usage-logs/__tests__/technical-usage.test.tsx',
    'src/features/wallet/__tests__/settings-state.spec.ts',
    'src/features/wallet/__tests__/settings-entry.ts',
  ],
}

export default config
