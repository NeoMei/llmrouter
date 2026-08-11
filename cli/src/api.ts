export type ConnectionOptions = {
  timeoutMs?: number;
  fetchImpl?: typeof fetch;
};

export function normalizeServerUrl(input: string): string {
  const parsed = new URL(input.trim());
  const isLoopback = ['localhost', '127.0.0.1', '::1'].includes(parsed.hostname);
  if (parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && isLoopback)) {
    throw new Error('Gateway URL must use HTTPS (HTTP is only allowed for localhost).');
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error('Gateway URL must not contain credentials, query parameters, or fragments.');
  }
  if (!['', '/', '/v1', '/v1/'].includes(parsed.pathname)) {
    throw new Error('Gateway URL path must be an origin or /v1.');
  }
  parsed.pathname = '';
  return parsed.toString().replace(/\/$/, '');
}

export async function verifyConnection(
  serverUrl: string,
  apiToken: string,
  options: ConnectionOptions = {}
): Promise<boolean> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? 10_000);
  try {
    const normalizedUrl = normalizeServerUrl(serverUrl);
    const resp = await (options.fetchImpl ?? fetch)(`${normalizedUrl}/v1/models`, {
      headers: { Authorization: `Bearer ${apiToken}` },
      signal: controller.signal,
    });
    return resp.ok;
  } catch {
    return false;
  } finally {
    clearTimeout(timeout);
  }
}
