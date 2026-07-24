import { Command } from 'commander';
import { input, password, select } from '@inquirer/prompts';
import { writeCodexConfig } from './codex.js';
import { writeClaudeConfig } from './claude.js';
import { verifyConnection } from './api.js';

export const initCommand = new Command('init')
  .description('Interactive setup for Codex CLI / Claude Code')
  .action(async () => {
    const serverUrl = await input({
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

    const ok = await verifyConnection(serverUrl, apiToken);
    if (!ok) {
      console.error('Connection failed. Check URL and token.');
      process.exit(1);
    }

    if (client === 'codex' || client === 'both') {
      await writeCodexConfig(serverUrl, apiToken);
      console.log('Codex CLI configured.');
    }
    if (client === 'claude' || client === 'both') {
      writeClaudeConfig(serverUrl, apiToken);
      console.log('Claude Code configured.');
    }

    console.log('Done.');
  });
