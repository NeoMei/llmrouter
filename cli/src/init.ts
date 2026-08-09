import { Command } from 'commander';
import { input, password, select } from '@inquirer/prompts';
import { writeCodexConfig } from './codex.js';
import { writeClaudeConfig } from './claude.js';
import { normalizeServerUrl, verifyConnection } from './api.js';
import { connectionFailureMessage } from './errors.js';

export const initCommand = new Command('init')
  .description('Interactive setup for Codex CLI / Claude Code')
  .action(async () => {
    const serverInput = await input({
      message: 'Server URL:',
      default: 'http://localhost:3000',
    });

    const apiToken = await password({ message: 'API Token:' });

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
      console.error(error instanceof Error ? error.message : 'Invalid server URL.');
      process.exitCode = 1;
      return;
    }

    const ok = await verifyConnection(serverUrl, apiToken);
    if (!ok) {
      console.error(connectionFailureMessage(serverUrl, apiToken));
      process.exitCode = 1;
      return;
    }

    if (client === 'codex' || client === 'both') {
      await writeCodexConfig(serverUrl, apiToken);
      console.log('Codex CLI configured.');
    }
    if (client === 'claude' || client === 'both') {
      await writeClaudeConfig(serverUrl, apiToken);
      console.log('Claude Code configured.');
    }

    console.log('Done.');
  });
