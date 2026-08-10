import * as assert from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { describe, it, beforeEach, afterEach } from 'node:test';
import { runDoctor, formatDoctorResult, type DoctorContext } from './doctor.js';
import { sha256 } from './client-state.js';

function makeStream(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let index = 0;
  return new ReadableStream({
    pull(controller) {
      if (index < chunks.length) {
        controller.enqueue(encoder.encode(chunks[index++]));
      } else {
        controller.close();
      }
    },
  });
}

function makeFetch(handlers: Record<string, (url: string, init?: RequestInit) => Response>): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = input.toString();
    for (const prefix of Object.keys(handlers)) {
      if (url.startsWith(prefix)) {
        return handlers[prefix](url, init);
      }
    }
    return new Response('not found', { status: 404 });
  }) as typeof fetch;
}

describe('doctor', () => {
  let home: string;
  beforeEach(() => {
    home = fs.mkdtempSync(path.join(os.tmpdir(), 'doctor-test-'));
  });
  afterEach(() => {
    fs.rmSync(home, { recursive: true, force: true });
  });

  it('reports PASS for all mocked checks', async () => {
    const model = 'deepseek/deepseek-v4-flash';
    const content = 'managed config';
    const managedPath = path.join(home, '.codex', 'config.toml');
    fs.mkdirSync(path.dirname(managedPath), { recursive: true, mode: 0o700 });
    fs.writeFileSync(managedPath, content, { mode: 0o600 });

    const receipt = {
      version: 1,
      client: 'codex',
      home,
      serverUrl: 'http://localhost:3000',
      files: [{ path: managedPath, originalHash: null, configuredHash: sha256(content), backupPath: '' }],
      shellProfile: null,
      envVars: {},
    };
    const receiptDir = path.join(home, '.codex-gateway', 'receipts');
    fs.mkdirSync(receiptDir, { recursive: true, mode: 0o700 });
    fs.writeFileSync(path.join(receiptDir, 'receipt-1.json'), JSON.stringify(receipt), { mode: 0o600 });

    const context: DoctorContext = {
      home,
      serverUrl: 'http://localhost:3000',
      apiToken: 'sk-test',
      model,
      fetchImpl: makeFetch({
        'http://localhost:3000/healthz': () => new Response('ok'),
        'http://localhost:3000/v1/models': () =>
          new Response(JSON.stringify({ data: [{ id: model }] }), { status: 200 }),
        'http://localhost:3000/v1/responses': () =>
          new Response(
            JSON.stringify({
              output: [{ type: 'message', role: 'assistant', content: 'hello' }],
            }),
            { status: 200 }
          ),
        'http://localhost:3000/v1/messages': () =>
          new Response(
            JSON.stringify({
              content: [{ type: 'text', text: 'hello' }],
            }),
            { status: 200 }
          ),
        'http://localhost:3000/v1/chat/completions': () =>
          new Response(makeStream(['data: {\n\n', 'data: [DONE]\n\n']), { status: 200 }),
      }),
    };

    const result = await runDoctor(context);
    assert.equal(result.failed, 0);
    assert.equal(result.checks.some((c) => c.status === 'FAIL'), false);
    const formatted = formatDoctorResult(result);
    assert.ok(formatted.includes('passed'));
    assert.ok(!formatted.includes('sk-'));
  });

  it('fails when the model is not visible', async () => {
    const context: DoctorContext = {
      home,
      serverUrl: 'http://localhost:3000',
      apiToken: 'sk-test',
      model: 'missing/model',
      fetchImpl: makeFetch({
        'http://localhost:3000/healthz': () => new Response('ok'),
        'http://localhost:3000/v1/models': () =>
          new Response(JSON.stringify({ data: [] }), { status: 200 }),
        'http://localhost:3000/v1/responses': () => new Response('{}', { status: 200 }),
        'http://localhost:3000/v1/messages': () => new Response('{}', { status: 200 }),
        'http://localhost:3000/v1/chat/completions': () =>
          new Response(makeStream(['data: [DONE]\n\n']), { status: 200 }),
      }),
    };
    const result = await runDoctor(context);
    assert.ok(result.checks.some((c) => c.name === 'model-visibility' && c.status === 'FAIL'));
  });

  it('fails on bad server url', async () => {
    const context: DoctorContext = {
      home,
      serverUrl: 'not-a-url',
      apiToken: 'sk-test',
      model: 'x',
      fetchImpl: makeFetch({}),
    };
    const result = await runDoctor(context);
    assert.ok(result.checks.some((c) => c.name === 'server-url' && c.status === 'FAIL'));
  });
});
