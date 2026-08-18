const assert = require('node:assert/strict');
const net = require('node:net');
const test = require('node:test');

const { createBackendRuntime } = require('../server-runtime');

async function availablePort() {
  const server = net.createServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert(address && typeof address !== 'string');
  await new Promise((resolve, reject) =>
    server.close((error) => (error ? reject(error) : resolve()))
  );
  return address.port;
}

test('wrapper starts backend, waits for readyz, and terminates the child', async () => {
  const port = await availablePort();
  const fixture = `
    const http = require('node:http');
    const server = http.createServer((request, response) => {
      response.writeHead(request.url === '/readyz' ? 200 : 404).end();
    });
    server.listen(Number(process.env.PORT), '127.0.0.1');
    process.on('SIGTERM', () => server.close(() => process.exit(0)));
  `;
  const runtime = createBackendRuntime({
    binaryPath: process.execPath,
    args: ['-e', fixture],
    env: { ...process.env, PORT: String(port) },
    readinessUrl: `http://127.0.0.1:${port}/readyz`,
    maxAttempts: 50,
    retryDelayMs: 20,
  });

  await runtime.start();
  const child = runtime.process;
  assert(child && child.pid);
  await runtime.stop({ graceMs: 1_000 });
  assert.equal(child.killed, true);
  assert.equal(runtime.process, null);
});

test('wrapper surfaces backend spawn errors', async () => {
  const runtime = createBackendRuntime({
    binaryPath: '/definitely/missing/new-api',
    readinessUrl: 'http://127.0.0.1:1/readyz',
    maxAttempts: 1,
    retryDelayMs: 1,
  });

  await assert.rejects(runtime.start(), /ENOENT|spawn/i);
  await runtime.stop({ graceMs: 10 });
});
