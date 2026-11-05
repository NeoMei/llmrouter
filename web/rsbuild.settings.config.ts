import { defineConfig } from '@rsbuild/core'

import base from './rsbuild.config'

export default defineConfig(async (context) => {
  const config = await base(context)
  config.source = {
    ...config.source,
    entry: {
      index: [
        './src/main.tsx',
        './src/features/wallet/__tests__/settings-entry.ts',
      ],
    },
  }
  return config
})
