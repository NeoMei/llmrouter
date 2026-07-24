export async function verifyConnection(serverUrl: string, apiToken: string): Promise<boolean> {
  try {
    const resp = await fetch(`${serverUrl}/v1/models`, {
      headers: { Authorization: `Bearer ${apiToken}` },
    });
    return resp.ok;
  } catch {
    return false;
  }
}
