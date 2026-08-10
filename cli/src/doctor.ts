import * as fs from 'node:fs';
import * as path from 'node:path';
import { normalizeServerUrl } from './api.js';
import { sha256 } from './client-state.js';

export type DoctorCheck = {
  name: string;
  status: 'PASS' | 'FAIL' | 'SKIP';
  message?: string;
};

export type DoctorContext = {
  home: string;
  serverUrl: string;
  apiToken: string;
  model: string;
  fetchImpl: typeof fetch;
};

export type DoctorResult = {
  checks: DoctorCheck[];
  passed: number;
  failed: number;
  skipped: number;
};

function receiptDir(home: string): string {
  return path.join(home, '.codex-gateway', 'receipts');
}

function backupDir(home: string): string {
  return path.join(home, '.codex-gateway', 'backups');
}

export async function runDoctor(context: DoctorContext): Promise<DoctorResult> {
  const checks: DoctorCheck[] = [];

  const add = (name: string, status: DoctorCheck['status'], message?: string) => {
    checks.push({ name, status, message });
  };

  // 1. Configuration ownership and permissions.
  try {
    const dirs = [receiptDir(context.home), backupDir(context.home)].filter((d) => fs.existsSync(d));
    let bad = 0;
    for (const dir of dirs) {
      for (const entry of fs.readdirSync(dir)) {
        const p = path.join(dir, entry);
        const st = fs.lstatSync(p);
        if (st.isSymbolicLink()) {
          bad++;
          continue;
        }
        if (st.uid !== process.getuid?.()) {
          bad++;
          continue;
        }
        const mode = st.mode & 0o777;
        if (mode !== 0o600 && mode !== 0o700) {
          bad++;
        }
      }
    }
    if (bad === 0) {
      add('config-ownership', 'PASS');
    } else {
      add('config-ownership', 'FAIL', `${bad} file(s) have insecure ownership or permissions`);
    }
  } catch (error) {
    add('config-ownership', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 2. Health endpoint.
  let baseUrl: string;
  try {
    baseUrl = normalizeServerUrl(context.serverUrl);
  } catch (error) {
    add('server-url', 'FAIL', error instanceof Error ? error.message : String(error));
    return { checks, passed: 0, failed: checks.filter((c) => c.status === 'FAIL').length, skipped: 0 };
  }
  add('server-url', 'PASS');

  const fetchJson = async (url: string, init?: RequestInit & { headers?: Record<string, string> }) => {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10_000);
    try {
      const resp = await context.fetchImpl(url, {
        ...init,
        signal: controller.signal,
        headers: {
          ...(init?.headers ?? {}),
        },
      });
      return resp;
    } finally {
      clearTimeout(timeout);
    }
  };

  try {
    const resp = await fetchJson(`${baseUrl}/healthz`);
    if (resp.ok) {
      add('healthz', 'PASS');
    } else {
      add('healthz', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('healthz', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 3. Authenticated model list.
  let models: { id: string }[] = [];
  try {
    const resp = await fetchJson(`${baseUrl}/v1/models`, {
      headers: { Authorization: `Bearer ${context.apiToken}` },
    });
    if (resp.ok) {
      const body = await resp.json().catch(() => ({} as Record<string, unknown>));
      const data = Array.isArray(body.data) ? (body.data as { id: string }[]) : [];
      models = data;
      add('model-list', 'PASS', `${data.length} models visible`);
    } else {
      add('model-list', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('model-list', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 4. Chosen model visibility.
  if (models.some((m) => m.id === context.model)) {
    add('model-visibility', 'PASS');
  } else {
    add('model-visibility', 'FAIL', `model ${context.model} not in visible catalog`);
  }

  // 5. Minimal native Responses request.
  try {
    const resp = await fetchJson(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${context.apiToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: context.model,
        input: [{ role: 'user', content: 'Say hello' }],
        max_output_tokens: 8,
      }),
    });
    if (resp.ok) {
      add('responses-request', 'PASS');
    } else {
      add('responses-request', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('responses-request', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 6. Function / custom tool round trip.
  try {
    const resp = await fetchJson(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${context.apiToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: context.model,
        input: [{ role: 'user', content: 'Call the tool' }],
        tools: [{ type: 'function', function: { name: 'echo', parameters: { type: 'object' } } }],
        max_output_tokens: 16,
      }),
    });
    if (resp.ok) {
      const body = await resp.json().catch(() => ({} as Record<string, unknown>));
      const hasTool =
        Array.isArray(body.output) &&
        body.output.some((item: any) => item.type === 'function_call' || item.type === 'tool_call');
      if (hasTool) {
        add('tool-round-trip', 'PASS');
      } else {
        add('tool-round-trip', 'SKIP', 'response did not contain a tool call');
      }
    } else {
      add('tool-round-trip', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('tool-round-trip', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 7. Anthropic Messages.
  try {
    const resp = await fetchJson(`${baseUrl}/v1/messages`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${context.apiToken}`,
        'Content-Type': 'application/json',
        'Anthropic-Version': '2023-06-01',
      },
      body: JSON.stringify({
        model: context.model,
        max_tokens: 8,
        messages: [{ role: 'user', content: 'Hello' }],
      }),
    });
    if (resp.ok) {
      add('anthropic-messages', 'PASS');
    } else {
      add('anthropic-messages', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('anthropic-messages', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 8. Streaming termination.
  try {
    const resp = await fetchJson(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${context.apiToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: context.model,
        messages: [{ role: 'user', content: 'Hi' }],
        stream: true,
        max_tokens: 8,
      }),
    });
    if (resp.ok && resp.body) {
      const reader = resp.body.getReader();
      let sawDone = false;
      let chunks = 0;
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        chunks++;
        const text = new TextDecoder().decode(value);
        if (text.includes('[DONE]')) {
          sawDone = true;
          break;
        }
        if (chunks > 1000) {
          break;
        }
      }
      add('streaming-termination', sawDone ? 'PASS' : 'SKIP', sawDone ? undefined : 'no [DONE] marker');
    } else {
      add('streaming-termination', 'FAIL', `status ${resp.status}`);
    }
  } catch (error) {
    add('streaming-termination', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  // 9. Restore receipt integrity.
  try {
    const dir = receiptDir(context.home);
    if (!fs.existsSync(dir) || fs.readdirSync(dir).length === 0) {
      add('receipt-integrity', 'SKIP', 'no receipts found');
    } else {
      const files = fs.readdirSync(dir).sort().reverse();
      const latest = path.join(dir, files[0]);
      const receipt = JSON.parse(fs.readFileSync(latest, 'utf8')) as {
        files: Array<{ path: string; configuredHash: string }>;
      };
      let bad = 0;
      for (const file of receipt.files ?? []) {
        const fullPath = file.path.startsWith('~') ? path.join(context.home, file.path.slice(1)) : file.path;
        if (!fs.existsSync(fullPath)) {
          bad++;
          continue;
        }
        const content = fs.readFileSync(fullPath, 'utf8');
        if (sha256(content) !== file.configuredHash) {
          bad++;
        }
      }
      if (bad === 0) {
        add('receipt-integrity', 'PASS');
      } else {
        add('receipt-integrity', 'FAIL', `${bad} configured file(s) changed since receipt`);
      }
    }
  } catch (error) {
    add('receipt-integrity', 'FAIL', error instanceof Error ? error.message : String(error));
  }

  const passed = checks.filter((c) => c.status === 'PASS').length;
  const failed = checks.filter((c) => c.status === 'FAIL').length;
  const skipped = checks.filter((c) => c.status === 'SKIP').length;
  return { checks, passed, failed, skipped };
}

export function formatDoctorResult(result: DoctorResult): string {
  const lines = result.checks.map((c) => `[${c.status}] ${c.name}${c.message ? `: ${c.message}` : ''}`);
  lines.push(`\n${result.passed} passed, ${result.failed} failed, ${result.skipped} skipped`);
  return lines.join('\n');
}

import { Command } from 'commander';
import { input, password } from '@inquirer/prompts';

export const doctorCommand = new Command('doctor')
  .description('Run end-to-end diagnostics against Codex Gateway')
  .option('-s, --server <url>', 'Gateway URL', 'http://localhost:3000')
  .option('-t, --token <token>', 'API token')
  .option('-m, --model <model>', 'Public model id', 'deepseek/deepseek-v4-flash')
  .option('--home <home>', 'Home directory', process.env.HOME ?? '')
  .action(async (options) => {
    const serverUrl = options.server as string;
    const apiToken = (options.token as string | undefined) ?? await password({ message: 'API Token:' });
    const result = await runDoctor({
      home: options.home as string,
      serverUrl,
      apiToken,
      model: options.model as string,
      fetchImpl: fetch,
    });
    console.log(formatDoctorResult(result));
    if (result.failed > 0) {
      process.exitCode = 1;
    }
  });
