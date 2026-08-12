import * as crypto from 'node:crypto';
import * as path from 'node:path';
import { runOnboard } from './onboard-helper.js';

export type Client = 'codex' | 'claude';
export type ClientReceipt = { client: Client; home: string; serverUrl: string; envVars: string[] };
export type ConfigureCodexOptions = { home: string; serverUrl: string; apiToken: string; model: string; shell?: string };
export type ConfigureClaudeOptions = { home: string; serverUrl: string; apiToken: string; shell?: string };

export function sha256(content: string): string { return crypto.createHash('sha256').update(content, 'utf8').digest('hex'); }

function configure(home: string, serverUrl: string, apiToken: string, clients: string, model?: string): ClientReceipt {
  if (!apiToken || /[\0\r\n]/.test(apiToken)) throw new Error('LLMROUTER_TOKEN is required');
  const env = process.env;
  const previous = env.LLMROUTER_TOKEN;
  env.LLMROUTER_TOKEN = apiToken;
  try {
    const args = ['--server-url', serverUrl, '--clients', clients];
    if (model) args.push('--model', model);
    runOnboard(path.resolve(home), 'install', args);
  } finally {
    if (previous === undefined) delete env.LLMROUTER_TOKEN; else env.LLMROUTER_TOKEN = previous;
  }
  return { client: clients === 'codex-cli' ? 'codex' : 'claude', home: path.resolve(home), serverUrl, envVars: ['LLMROUTER_TOKEN'] };
}
export async function configureCodex(options: ConfigureCodexOptions): Promise<ClientReceipt> { return configure(options.home, options.serverUrl, options.apiToken, 'codex-cli', options.model); }
export async function configureClaude(options: ConfigureClaudeOptions): Promise<ClientReceipt> { return configure(options.home, options.serverUrl, options.apiToken, 'claude-code'); }
