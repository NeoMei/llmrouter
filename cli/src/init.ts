import * as os from 'node:os';
import { input, password, select } from '@inquirer/prompts';
import { Command } from 'commander';
import { normalizeServerUrl } from './api.js';
import { runOnboard, safeError } from './onboard-helper.js';
export const initCommand = new Command('init').description('Configure supported clients through trusted LLMAPI onboarding').action(async () => {
  try {
    const server = normalizeServerUrl(await input({ message: 'LLMAPI server URL:', default: 'http://localhost:3000' }));
    const token = process.env.LLMAPI_TOKEN ?? await password({ message: 'LLMAPI token:' });
    if (!token) throw new Error('LLMAPI_TOKEN is required');
    const client = await select({ message: 'Configure:', choices: [{ name: 'Codex CLI', value: 'codex-cli' }, { name: 'Claude Code', value: 'claude-code' }, { name: 'Both', value: 'codex-cli,claude-code' }] });
    const model = client.includes('codex-cli') ? await input({ message: 'Codex model:', default: 'deepseek/deepseek-v4-flash' }) : '';
    const previous = process.env.LLMAPI_TOKEN; process.env.LLMAPI_TOKEN = token;
    try { const home = process.env.HOME ?? os.homedir(); for (const selected of client.split(',')) { runOnboard(home, 'install', ['--server-url', server, '--clients', selected, ...(selected === 'codex-cli' && model ? ['--model', model] : [])]); } } finally { if (previous === undefined) delete process.env.LLMAPI_TOKEN; else process.env.LLMAPI_TOKEN = previous; }
  } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
