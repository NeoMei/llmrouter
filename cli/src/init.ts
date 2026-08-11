import { input, password, select } from '@inquirer/prompts';
import { Command } from 'commander';

import { normalizeServerUrl, verifyConnection } from './api.js';
import { configureClaude, configureCodex, detectLegacyState } from './client-state.js';
import { connectionFailureMessage } from './errors.js';

export const initCommand = new Command('init')
  .description('Interactive setup for supported LLMAPI clients')
  .action(async () => {
    const serverInput = await input({
      message: 'LLMAPI server URL:',
      default: 'http://localhost:3000',
    });
    const apiToken = process.env.LLMAPI_TOKEN ?? await password({ message: 'LLMAPI token:' });
    const client = await select({
      message: 'Configure:',
      choices: [
        { name: 'Codex CLI', value: 'codex' },
        { name: 'Claude Code', value: 'claude' },
        { name: 'Both', value: 'both' },
      ],
    });

    let serverUrl: string;
    try {
      serverUrl = normalizeServerUrl(serverInput);
    } catch (error) {
      console.error(error instanceof Error ? error.message : 'Invalid LLMAPI server URL.');
      process.exitCode = 1;
      return;
    }
    if (!apiToken) {
      console.error('LLMAPI_TOKEN is required.');
      process.exitCode = 1;
      return;
    }
    if (!await verifyConnection(serverUrl, apiToken)) {
      console.error(connectionFailureMessage(serverUrl, apiToken));
      process.exitCode = 1;
      return;
    }

    const home = process.env.HOME ?? '';
    if (detectLegacyState(home)) {
      console.warn('Legacy Codex Gateway state was detected and left unchanged.');
    }
    if (client === 'codex' || client === 'both') {
      const model = await input({ message: 'Codex model:', default: 'deepseek/deepseek-v4-flash' });
      await configureCodex({ home, serverUrl, apiToken, model });
      console.log('Codex CLI configured for LLMAPI.');
    }
    if (client === 'claude' || client === 'both') {
      await configureClaude({ home, serverUrl, apiToken });
      console.log('Claude Code configured for LLMAPI.');
    }
    console.log('Set LLMAPI_TOKEN before launching configured clients.');
  });
