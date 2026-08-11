import * as os from 'node:os';
import { Command } from 'commander';
import { runOnboard, safeError } from './onboard-helper.js';
export const uninstallCommand = new Command('uninstall').description('Uninstall a managed LLMAPI receipt').argument('[receipt]', 'receipt ID').option('--home <home>', 'home directory', process.env.HOME ?? os.homedir()).action((receipt: string | undefined, options: { home: string }) => {
  try { runOnboard(options.home, 'uninstall', receipt ? [receipt] : []); } catch (error) { console.error(safeError(error)); process.exitCode = 1; }
});
