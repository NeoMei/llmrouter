import * as os from 'node:os';
import { input, password, select } from '@inquirer/prompts';
import { Command } from 'commander';
import { normalizeServerUrl } from './api.js';
import { runOnboard, safeError } from './onboard-helper.js';
export const initCommand = new Command('init').description('Configure supported clients through trusted LLMROUTER onboarding').action(async () => {
  try {
    const server = normalizeServerUrl(await input({ message: 'LLMROUTER server URL:', default: 'http://localhost:3000' }));
    const token = process.env.LLMROUTER_TOKEN ?? await password({ message: 'LLMROUTER token:' });
    if (!token) throw new Error('LLMROUTER_TOKEN is required');
    const client = await select({ message: 'Configure:', choices: [{ name: 'Codex CLI', value: 'codex-cli' }, { name: 'Claude Code', value: 'claude-code' }, { name: 'Both', value: 'codex-cli,claude-code' }] });
    const model = client.includes('codex-cli') ? await input({ message: 'Codex model:', default: 'deepseek/deepseek-v4-flash' }) : '';
    const previous = process.env.LLMROUTER_TOKEN; process.env.LLMROUTER_TOKEN = token;
    try { const home = process.env.HOME ?? os.homedir(); for (const selected of client.split(',')) { runOnboard(home, 'install', ['--server-url', server, '--clients', selected, ...(selected === 'codex-cli' && model ? ['--model', model] : [])]); } } finally { if (previous === undefined) delete process.env.LLMROUTER_TOKEN; else process.env.LLMROUTER_TOKEN = previous; }
  } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
