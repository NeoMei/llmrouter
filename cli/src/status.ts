import { Command } from 'commander';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { normalizeServerUrl, verifyConnection } from './api.js';
import { connectionFailureMessage } from './errors.js';

export const statusCommand = new Command('status')
  .description('Verify connection to Codex Gateway')
  .action(async () => {
    const configPath = path.join(os.homedir(), '.codex', 'config.toml');
    if (!fs.existsSync(configPath)) {
      console.error('No config found. Run `codex-gateway init` first.');
      process.exitCode = 1;
      return;
    }

    // ponytail: simple regex parse — we wrote this file ourselves, no TOML dep needed
    const config = fs.readFileSync(configPath, 'utf8');
    const baseUrlMatch = config.match(/base_url\s*=\s*"([^"]+)"/);
    if (!baseUrlMatch) {
      console.error('Could not parse base_url from config.');
      process.exitCode = 1;
      return;
    }

    let serverUrl: string;
    try {
      serverUrl = normalizeServerUrl(baseUrlMatch[1]);
    } catch {
      console.error('Configured base_url is invalid.');
      process.exitCode = 1;
      return;
    }
    const apiKey = process.env.CODEX_GATEWAY_KEY;

    if (!apiKey) {
      console.error('CODEX_GATEWAY_KEY not set. Restart your shell or source your profile.');
      process.exitCode = 1;
      return;
    }

    const ok = await verifyConnection(serverUrl, apiKey);
    if (ok) {
      console.log(`Connected to ${serverUrl} OK`);
    } else {
      console.error(connectionFailureMessage(serverUrl, apiKey));
      process.exitCode = 1;
    }
  });
