import * as os from 'node:os';
import { Command } from 'commander';
import { runOnboard, safeError } from './onboard-helper.js';
export const statusCommand = new Command('status').description('Verify LLMROUTER through trusted onboarding diagnostics').option('-s, --server <url>', 'LLMROUTER server URL').option('--home <home>', 'home directory', process.env.HOME ?? os.homedir()).action((options: { server?: string; home: string }) => {
  try { runOnboard(options.home, 'doctor', options.server ? ['--server-url', options.server, '--clients', 'codex-cli'] : ['--clients', 'codex-cli']); } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
