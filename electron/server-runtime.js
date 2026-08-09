const { spawn } = require('node:child_process');
const http = require('node:http');
const https = require('node:https');

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function probe(url, timeoutMs) {
  return new Promise((resolve, reject) => {
    const client = url.startsWith('https:') ? https : http;
    const request = client.get(url, { timeout: timeoutMs }, (response) => {
      response.resume();
      if (response.statusCode >= 200 && response.statusCode < 300) {
        resolve();
      } else {
        reject(new Error(`Readiness probe returned HTTP ${response.statusCode}`));
      }
    });
    request.once('timeout', () => {
      request.destroy(new Error('Readiness probe timed out'));
    });
    request.once('error', reject);
  });
}

async function waitForReady({
  url,
  maxAttempts = 60,
  retryDelayMs = 500,
  requestTimeoutMs = 2_000,
}) {
  let lastError;
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    try {
      await probe(url, requestTimeoutMs);
      return;
    } catch (error) {
      lastError = error;
      if (attempt < maxAttempts) {
        await delay(retryDelayMs);
      }
    }
  }
  throw new Error(
    `Backend did not become ready after ${maxAttempts} attempts: ${lastError?.message ?? 'unknown error'}`
  );
}

function waitForExit(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve();
  }
  return new Promise((resolve) => child.once('close', resolve));
}

function createBackendRuntime(options) {
  let child = null;
  let startPromise = null;
  let stopPromise = null;

  const runtime = {
    get process() {
      return child;
    },

    async start() {
      if (startPromise) return startPromise;
      startPromise = (async () => {
        child = (options.spawnImpl ?? spawn)(
          options.binaryPath,
          options.args ?? [],
          {
            cwd: options.cwd,
            env: options.env ?? process.env,
            stdio: ['ignore', 'pipe', 'pipe'],
            windowsHide: true,
          }
        );

        child.stdout?.on('data', (data) => options.onStdout?.(data.toString()));
        child.stderr?.on('data', (data) => options.onStderr?.(data.toString()));

        const spawnFailure = new Promise((_, reject) => {
          child.once('error', reject);
          child.once('exit', (code, signal) => {
            reject(
              new Error(
                `Backend exited before readiness (code=${code}, signal=${signal})`
              )
            );
          });
        });

        try {
          await Promise.race([
            waitForReady({
              url: options.readinessUrl,
              maxAttempts: options.maxAttempts,
              retryDelayMs: options.retryDelayMs,
              requestTimeoutMs: options.requestTimeoutMs,
            }),
            spawnFailure,
          ]);
          return child;
        } catch (error) {
          await runtime.stop({ graceMs: 250 });
          throw error;
        }
      })();
      return startPromise;
    },

    async stop({ graceMs = 5_000 } = {}) {
      if (stopPromise) return stopPromise;
      stopPromise = (async () => {
        const processToStop = child;
        if (!processToStop) return;
        if (processToStop.exitCode === null && processToStop.signalCode === null) {
          processToStop.kill('SIGTERM');
          await Promise.race([waitForExit(processToStop), delay(graceMs)]);
          if (
            processToStop.exitCode === null &&
            processToStop.signalCode === null
          ) {
            processToStop.kill('SIGKILL');
            await waitForExit(processToStop);
          }
        }
        child = null;
      })();
      return stopPromise;
    },
  };

  return runtime;
}

module.exports = { createBackendRuntime, waitForReady };
