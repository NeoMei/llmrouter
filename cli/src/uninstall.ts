import * as os from 'node:os';

import { Command } from 'commander';

import { uninstallClient, type Client } from './client-state.js';

export const uninstallCommand = new Command('uninstall')
  .description('Remove managed LLMAPI configuration for one client')
  .argument('[client]', 'codex or claude', 'codex')
  .option('--home <home>', 'home directory', process.env.HOME ?? os.homedir())
  .action(async (client: string, options: { home: string }) => {
    if (client !== 'codex' && client !== 'claude') {
      console.error('Client must be either codex or claude.');
      process.exitCode = 1;
      return;
    }
    try {
      await uninstallClient(options.home, client as Client);
      console.log(`LLMAPI configuration removed for ${client}.`);
    } catch (error) {
      console.error(error instanceof Error ? error.message : 'Unable to uninstall LLMAPI configuration.');
      process.exitCode = 1;
    }
  });
