import { normalizeServerUrl } from './api.js';
import { ensureEnvVar, type ConfigWriteOptions } from './codex.js';

export async function writeClaudeConfig(
  serverUrl: string,
  apiToken: string,
  options: ConfigWriteOptions = {}
): Promise<void> {
  const normalizedUrl = normalizeServerUrl(serverUrl);
  await ensureEnvVar('ANTHROPIC_BASE_URL', normalizedUrl, options);
  await ensureEnvVar('ANTHROPIC_API_KEY', apiToken, options);
  options.log?.('Claude Code environment configured in the selected shell profile.');
}
