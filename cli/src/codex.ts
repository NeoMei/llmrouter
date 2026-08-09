import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

import { normalizeServerUrl } from './api.js';

export type ConfigWriteOptions = {
  homeDir?: string;
  shell?: string;
  log?: (message: string) => void;
};

function resolveHome(options: ConfigWriteOptions): string {
  return options.homeDir ?? os.homedir();
}

function writeSecureFile(filePath: string, content: string): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true, mode: 0o700 });
  fs.writeFileSync(filePath, content, { encoding: 'utf8', mode: 0o600 });
  fs.chmodSync(filePath, 0o600);
}

function shellQuote(value: string): string {
  if (/\0|\r|\n/.test(value)) {
    throw new Error('Environment values must not contain control characters.');
  }
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export async function writeCodexConfig(
  serverUrl: string,
  apiToken: string,
  options: ConfigWriteOptions = {}
): Promise<void> {
  const homeDir = resolveHome(options);
  const configDir = path.join(homeDir, '.codex');
  const configPath = path.join(configDir, 'config.toml');
  const normalizedUrl = normalizeServerUrl(serverUrl);

  fs.mkdirSync(configDir, { recursive: true, mode: 0o700 });
  fs.chmodSync(configDir, 0o700);

  if (fs.existsSync(configPath)) {
    const backupPath = `${configPath}.bak`;
    fs.copyFileSync(configPath, backupPath);
    fs.chmodSync(backupPath, 0o600);
  }

  const config = `model = "deepseek-v4-flash"
model_provider = "gateway"

[model_providers.gateway]
name = "Codex Gateway"
base_url = "${normalizedUrl}/v1"
wire_api = "responses"
env_key = "CODEX_GATEWAY_KEY"
`;

  writeSecureFile(configPath, config);
  await ensureEnvVar('CODEX_GATEWAY_KEY', apiToken, options);
}

export async function ensureEnvVar(
  key: string,
  value: string,
  options: ConfigWriteOptions = {}
): Promise<void> {
  if (!/^[A-Z][A-Z0-9_]*$/.test(key)) {
    throw new Error('Invalid environment variable name.');
  }
  const profile = detectShellProfile(options);
  const line = `export ${key}=${shellQuote(value)}`;
  let content = fs.existsSync(profile) ? fs.readFileSync(profile, 'utf8') : '';
  const regex = new RegExp(`^export ${key}=.*$`, 'm');

  if (regex.test(content)) {
    content = content.replace(regex, line);
    options.log?.(`Updated ${key} in ${profile}`);
  } else {
    const separator = content.length > 0 && !content.endsWith('\n') ? '\n' : '';
    content = `${content}${separator}${line}\n`;
    options.log?.(`Added ${key} to ${profile}`);
  }

  writeSecureFile(profile, content);
}

function detectShellProfile(options: ConfigWriteOptions): string {
  const shell = options.shell ?? process.env.SHELL ?? '';
  const home = resolveHome(options);
  if (shell.includes('bash')) return path.join(home, '.bashrc');
  return path.join(home, '.zshrc');
}
