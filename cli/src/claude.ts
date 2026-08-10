import * as os from 'node:os';

import { configureClaude, type ClientReceipt } from './client-state.js';

export type ConfigWriteOptions = {
  homeDir?: string;
  shell?: string;
  log?: (message: string) => void;
};

export type { ClientReceipt };

export async function writeClaudeConfig(
  serverUrl: string,
  apiToken: string,
  options: ConfigWriteOptions = {}
): Promise<void> {
  const home = options.homeDir ?? os.homedir();
  await configureClaude({ home, serverUrl, apiToken, shell: options.shell });
  options.log?.('Claude Code environment configured in the selected shell profile.');
}
