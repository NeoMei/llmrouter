import * as os from 'node:os';
import { Command } from 'commander';
import { runOnboard, safeError } from './onboard-helper.js';
export const doctorCommand = new Command('doctor').description('Run trusted LLMAPI onboarding diagnostics').option('-s, --server <url>', 'LLMAPI server URL').option('-m, --model <model>', 'public model ID').option('--home <home>', 'home directory', process.env.HOME ?? os.homedir()).action((options: { server?: string; model?: string; home: string }) => {
  try { const args = [...(options.server ? ['--server-url', options.server] : []), ...(options.model ? ['--model', options.model] : [])]; runOnboard(options.home, 'doctor', args); } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
