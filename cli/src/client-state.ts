import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

export type ClientReceiptFile = {
  path: string;
  originalHash: string | null;
  configuredHash: string;
  backupPath: string;
};

export type ClientReceipt = {
  version: 1;
  client: 'codex' | 'claude';
  home: string;
  serverUrl: string;
  files: ClientReceiptFile[];
  shellProfile: string | null;
  envVars: Record<string, string>;
};

export type ConfigureCodexOptions = {
  home: string;
  serverUrl: string;
  apiToken: string;
  model: string;
  shell?: string;
};

export type ConfigureClaudeOptions = {
  home: string;
  serverUrl: string;
  apiToken: string;
  shell?: string;
};

const MANAGED_BLOCK_BEGIN = '# CODEX-GATEWAY:BEGIN';
const MANAGED_BLOCK_END = '# CODEX-GATEWAY:END';
const RECEIPT_DIR = '.codex-gateway/receipts';
const BACKUP_DIR = '.codex-gateway/backups';

export function sha256(content: string): string {
  return crypto.createHash('sha256').update(content, 'utf8').digest('hex');
}

export function ensurePrivateDir(dir: string): void {
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  fs.chmodSync(dir, 0o700);
}

export function atomicWrite(filePath: string, content: string, mode = 0o600): void {
  ensurePrivateDir(path.dirname(filePath));
  const dir = path.dirname(filePath);
  const temp = path.join(dir, `.tmp-${path.basename(filePath)}-${Date.now()}`);
  fs.writeFileSync(temp, content, { encoding: 'utf8', mode });
  try {
    fs.fsyncSync(fs.openSync(temp, 'r'));
  } catch {
    // fsync is best-effort on some filesystems
  }
  fs.renameSync(temp, filePath);
  fs.chmodSync(filePath, mode);
}

export function assertOwnership(filePath: string): void {
  if (!fs.existsSync(filePath)) return;
  const lstat = fs.lstatSync(filePath);
  if (lstat.isSymbolicLink()) {
    throw new Error(`refusing to modify symlink ${filePath}`);
  }
  if (lstat.uid !== process.getuid?.()) {
    throw new Error(`refusing to modify file owned by another user: ${filePath}`);
  }
}

export function readOwnedFile(filePath: string): string | null {
  if (!fs.existsSync(filePath)) return null;
  assertOwnership(filePath);
  return fs.readFileSync(filePath, 'utf8');
}

export function shellQuote(value: string): string {
  if (/\0|\r|\n/.test(value)) {
    throw new Error('Environment values must not contain control characters.');
  }
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function managedBlock(vars: Record<string, string>): string {
  const lines = Object.entries(vars).map(
    ([key, value]) => `export ${key}=${shellQuote(value)}`
  );
  return [MANAGED_BLOCK_BEGIN, ...lines, MANAGED_BLOCK_END].join('\n');
}

export function applyManagedBlock(profile: string, block: string): string {
  const regex = new RegExp(
    `\\n?${escapeRegex(MANAGED_BLOCK_BEGIN)}[\\s\\S]*?${escapeRegex(MANAGED_BLOCK_END)}\\n?`,
    'g'
  );
  let cleaned = profile.replace(regex, '\n');
  if (cleaned.length > 0 && !cleaned.endsWith('\n')) cleaned += '\n';
  cleaned += block + '\n';
  return cleaned;
}

export function removeManagedBlock(profile: string): string {
  const regex = new RegExp(
    `\\n?${escapeRegex(MANAGED_BLOCK_BEGIN)}[\\s\\S]*?${escapeRegex(MANAGED_BLOCK_END)}\\n?`,
    'g'
  );
  return profile.replace(regex, '\n');
}

function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

export function normalizeServerUrl(url: string): string {
  let trimmed = url.trim();
  if (trimmed.endsWith('/')) trimmed = trimmed.slice(0, -1);
  return trimmed;
}

export function backupPath(home: string, hash: string): string {
  return path.join(home, BACKUP_DIR, hash.slice(0, 2), hash.slice(2));
}

export function storeBackup(home: string, content: string | null): string {
  const data = content ?? '';
  const hash = sha256(data);
  const bp = backupPath(home, hash);
  ensurePrivateDir(path.dirname(bp));
  if (!fs.existsSync(bp)) {
    atomicWrite(bp, data, 0o600);
  }
  return bp;
}

export function saveReceipt(receipt: ClientReceipt): void {
  const receiptDir = path.join(receipt.home, RECEIPT_DIR);
  ensurePrivateDir(receiptDir);
  const name = `${receipt.client}-${Date.now()}-${crypto.randomBytes(4).toString('hex')}.json`;
  const filePath = path.join(receiptDir, name);
  atomicWrite(filePath, JSON.stringify(receipt, null, 2), 0o600);
}

export function detectShellProfile(home: string, shell?: string): string {
  const shellPath = shell ?? process.env.SHELL ?? '';
  if (shellPath.includes('bash')) return path.join(home, '.bashrc');
  return path.join(home, '.zshrc');
}

export async function configureCodex(options: ConfigureCodexOptions): Promise<ClientReceipt> {
  const { home, serverUrl, apiToken, model } = options;
  const normalizedUrl = normalizeServerUrl(serverUrl);

  const codexDir = path.join(home, '.codex');
  ensurePrivateDir(codexDir);

  const configPath = path.join(codexDir, 'config.toml');
  assertOwnership(configPath);
  const originalConfig = readOwnedFile(configPath);

  const config = `model = "${model}"
model_provider = "gateway"

[model_providers.gateway]
name = "Codex Gateway"
base_url = "${normalizedUrl}/v1"
wire_api = "responses"
env_key = "CODEX_GATEWAY_KEY"
`;

  const backupConfig = storeBackup(home, originalConfig);
  atomicWrite(configPath, config, 0o600);

  const shellProfile = detectShellProfile(home, options.shell);
  const originalProfile = readOwnedFile(shellProfile);
  const block = managedBlock({ CODEX_GATEWAY_KEY: apiToken });
  const newProfile = applyManagedBlock(originalProfile ?? '', block);
  const backupProfile = storeBackup(home, originalProfile);
  atomicWrite(shellProfile, newProfile, 0o600);

  const receipt: ClientReceipt = {
    version: 1,
    client: 'codex',
    home,
    serverUrl: normalizedUrl,
    files: [
      {
        path: configPath,
        originalHash: originalConfig === null ? null : sha256(originalConfig),
        configuredHash: sha256(config),
        backupPath: backupConfig,
      },
      {
        path: shellProfile,
        originalHash: originalProfile === null ? null : sha256(originalProfile),
        configuredHash: sha256(newProfile),
        backupPath: backupProfile,
      },
    ],
    shellProfile,
    envVars: { CODEX_GATEWAY_KEY: apiToken },
  };
  saveReceipt(receipt);
  return receipt;
}

export async function configureClaude(options: ConfigureClaudeOptions): Promise<ClientReceipt> {
  const { home, serverUrl, apiToken } = options;
  const normalizedUrl = normalizeServerUrl(serverUrl);

  const shellProfile = detectShellProfile(home, options.shell);
  const originalProfile = readOwnedFile(shellProfile);
  const block = managedBlock({
    ANTHROPIC_BASE_URL: normalizedUrl,
    ANTHROPIC_API_KEY: apiToken,
  });
  const newProfile = applyManagedBlock(originalProfile ?? '', block);
  const backupProfile = storeBackup(home, originalProfile);
  atomicWrite(shellProfile, newProfile, 0o600);

  const receipt: ClientReceipt = {
    version: 1,
    client: 'claude',
    home,
    serverUrl: normalizedUrl,
    files: [
      {
        path: shellProfile,
        originalHash: originalProfile === null ? null : sha256(originalProfile),
        configuredHash: sha256(newProfile),
        backupPath: backupProfile,
      },
    ],
    shellProfile,
    envVars: { ANTHROPIC_BASE_URL: normalizedUrl, ANTHROPIC_API_KEY: apiToken },
  };
  saveReceipt(receipt);
  return receipt;
}

export async function restoreClient(receipt: ClientReceipt): Promise<void> {
  for (const entry of receipt.files) {
    assertOwnership(entry.path);
    const current = readOwnedFile(entry.path);
    if (current !== null && sha256(current) !== entry.configuredHash) {
      throw new Error(
        `refusing to restore ${entry.path}: file has changed since configuration (expected hash ${entry.configuredHash}, got ${sha256(current)})`
      );
    }
    if (!entry.backupPath || !fs.existsSync(entry.backupPath)) {
      if (entry.originalHash === null) {
        if (fs.existsSync(entry.path)) fs.unlinkSync(entry.path);
        continue;
      }
      throw new Error(`backup missing for ${entry.path}; cannot restore`);
    }
    const backup = fs.readFileSync(entry.backupPath, 'utf8');
    if (entry.originalHash === null) {
      if (fs.existsSync(entry.path)) fs.unlinkSync(entry.path);
    } else {
      atomicWrite(entry.path, backup, 0o600);
    }
  }
}
