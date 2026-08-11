import * as os from 'node:os';

import { Command } from 'commander';

import { loadReceipt, restoreClient } from './client-state.js';

export const restoreCommand = new Command('restore')
  .description('Restore a managed LLMAPI receipt (defaults to the latest receipt)')
  .argument('[receipt]', 'receipt name')
  .option('--home <home>', 'home directory', process.env.HOME ?? os.homedir())
  .action(async (receipt: string | undefined, options: { home: string }) => {
    try {
      await restoreClient(loadReceipt(options.home, receipt));
      console.log('LLMAPI configuration restored.');
    } catch (error) {
      console.error(error instanceof Error ? error.message : 'Unable to restore LLMAPI configuration.');
      process.exitCode = 1;
    }
  });
