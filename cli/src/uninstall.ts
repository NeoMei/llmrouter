import * as os from 'node:os';
import { Command } from 'commander';
import { runOnboard, safeError } from './onboard-helper.js';
const clients: Record<string, string> = { codex: 'codex-cli', claude: 'claude-code' };
export const uninstallCommand = new Command('uninstall').description('Uninstall one managed LLMROUTER client').argument('<client>', 'codex or claude').option('--home <home>', 'home directory', process.env.HOME ?? os.homedir()).action((client: string, options: { home: string }) => {
  try { if (!clients[client]) throw new Error('client must be codex or claude'); runOnboard(options.home, 'uninstall', ['--client', clients[client]]); } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
