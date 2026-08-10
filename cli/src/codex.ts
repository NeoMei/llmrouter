import * as os from 'node:os';

import { configureCodex, type ClientReceipt } from './client-state.js';

export type ConfigWriteOptions = {
  homeDir?: string;
  shell?: string;
  log?: (message: string) => void;
};

export type { ClientReceipt };

export async function writeCodexConfig(
  serverUrl: string,
  apiToken: string,
  options: ConfigWriteOptions = {},
  model = 'deepseek-v4-flash'
): Promise<void> {
  const home = options.homeDir ?? os.homedir();
  await configureCodex({ home, serverUrl, apiToken, model, shell: options.shell });
  options.log?.('Codex CLI configured.');
}
