export function redactSecrets(message: string, secrets: readonly string[]): string {
  return secrets
    .filter(Boolean)
    .reduce((redacted, secret) => redacted.replaceAll(secret, '[REDACTED]'), message);
}

export function connectionFailureMessage(serverUrl: string, apiToken?: string): string {
  return redactSecrets(`Connection to ${serverUrl} failed. Check the URL and token.`, [
    apiToken ?? '',
  ]);
}
