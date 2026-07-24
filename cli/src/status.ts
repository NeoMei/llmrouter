import { Command } from 'commander';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { verifyConnection } from './api.js';

export const statusCommand = new Command('status')
  .description('Verify connection to Codex Gateway')
  .action(async () => {
    const configPath = path.join(os.homedir(), '.codex', 'config.toml');
    if (!fs.existsSync(configPath)) {
      console.error('No config found. Run `codex-gateway init` first.');
      process.exit(1);
    }

    // ponytail: simple regex parse — we wrote this file ourselves, no TOML dep needed
    const config = fs.readFileSync(configPath, 'utf8');
    const baseUrlMatch = config.match(/base_url\s*=\s*"([^"]+)"/);
    if (!baseUrlMatch) {
      console.error('Could not parse base_url from config.');
      process.exit(1);
    }

    const serverUrl = baseUrlMatch[1].replace(/\/v1\/?$/, '');
    const apiKey = process.env.CODEX_GATEWAY_KEY;

    if (!apiKey) {
      console.error('CODEX_GATEWAY_KEY not set. Restart your shell or source your profile.');
      process.exit(1);
    }

    const ok = await verifyConnection(serverUrl, apiKey);
    if (ok) {
      console.log(`Connected to ${serverUrl} OK`);
    } else {
      console.error(`Connection to ${serverUrl} failed.`);
      process.exit(1);
    }
  });
