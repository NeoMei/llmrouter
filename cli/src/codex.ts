import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';

export async function writeCodexConfig(serverUrl: string, apiToken: string): Promise<void> {
  const configDir = path.join(os.homedir(), '.codex');
  const configPath = path.join(configDir, 'config.toml');

  fs.mkdirSync(configDir, { recursive: true });

  // Backup existing config before overwriting
  if (fs.existsSync(configPath)) {
    fs.copyFileSync(configPath, `${configPath}.bak`);
  }

  const config = `model = "deepseek-v4-flash"
model_provider = "gateway"

[model_providers.gateway]
name = "Codex Gateway"
base_url = "${serverUrl}/v1"
wire_api = "responses"
env_key = "CODEX_GATEWAY_KEY"
`;

  fs.writeFileSync(configPath, config);

  await ensureEnvVar('CODEX_GATEWAY_KEY', apiToken);
}

/**
 * Write or update an `export KEY="value"` line in the user's shell profile.
 * Idempotent: replaces an existing line rather than appending a duplicate.
 */
export async function ensureEnvVar(key: string, value: string): Promise<void> {
  const profile = detectShellProfile();
  const line = `export ${key}="${value}"`;

  if (fs.existsSync(profile)) {
    const content = fs.readFileSync(profile, 'utf8');
    const regex = new RegExp(`^export ${key}=.*$`, 'm');
    if (regex.test(content)) {
      fs.writeFileSync(profile, content.replace(regex, line));
      console.log(`  Updated ${key} in ${profile}`);
      return;
    }
  }

  fs.appendFileSync(profile, `${line}\n`);
  console.log(`  Added ${key} to ${profile}`);
}

function detectShellProfile(): string {
  const shell = process.env.SHELL ?? '';
  const home = os.homedir();
  if (shell.includes('zsh')) return path.join(home, '.zshrc');
  if (shell.includes('bash')) return path.join(home, '.bashrc');
  // ponytail: default to .zshrc — macOS default shell since Catalina
  return path.join(home, '.zshrc');
}
