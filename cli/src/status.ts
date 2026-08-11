import { Command } from 'commander';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

import { normalizeServerUrl, verifyConnection } from './api.js';
import { connectionFailureMessage } from './errors.js';

export const statusCommand = new Command('status')
  .description('Verify connection to LLMAPI')
  .action(async () => {
    const configPath = path.join(os.homedir(), '.codex', 'config.toml');
    if (!fs.existsSync(configPath)) {
      console.error('No LLMAPI Codex configuration found. Run `llmapi init` first.');
      process.exitCode = 1;
      return;
    }
    const config = fs.readFileSync(configPath, 'utf8');
    const provider = config.match(/\[model_providers\.llmapi\][\s\S]*?base_url\s*=\s*"([^"]+)"/);
    if (!provider) {
      console.error('Could not parse the LLMAPI base_url from Codex configuration.');
      process.exitCode = 1;
      return;
    }

    let serverUrl: string;
    try {
      serverUrl = normalizeServerUrl(provider[1]);
    } catch {
      console.error('Configured LLMAPI base_url is invalid.');
      process.exitCode = 1;
      return;
    }
    const apiToken = process.env.LLMAPI_TOKEN;
    if (!apiToken) {
      console.error('LLMAPI_TOKEN is not set. Restart your shell or export it before running clients.');
      process.exitCode = 1;
      return;
    }

    if (await verifyConnection(serverUrl, apiToken)) {
      console.log(`Connected to ${serverUrl} OK`);
    } else {
      console.error(connectionFailureMessage(serverUrl, apiToken));
      process.exitCode = 1;
    }
  });
