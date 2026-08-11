import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';

import { normalizeServerUrl } from './api.js';

export type Client = 'codex' | 'claude';

export type ClientReceiptFile = {
  path: string;
  originalHash: string | null;
  configuredHash: string;
  backupPath: string;
};

export type ClientReceipt = {
  version: 1;
  client: Client;
  home: string;
  serverUrl: string;
  files: ClientReceiptFile[];
  shellProfile: string | null;
  envVars: string[];
  legacyStateDetected?: boolean;
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

const STATE_ROOT = '.llmapi';
const LEGACY_STATE_ROOT = '.codex-gateway';
const RECEIPT_DIR = `${STATE_ROOT}/receipts`;
const BACKUP_DIR = `${STATE_ROOT}/backups`;

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
    const fd = fs.openSync(temp, 'r');
    fs.fsyncSync(fd);
    fs.closeSync(fd);
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
    throw new Error(`refusing to modify symlink ${displayPath(filePath)}`);
  }
  if (lstat.uid !== process.getuid?.()) {
    throw new Error(`refusing to modify file owned by another user: ${displayPath(filePath)}`);
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
  return `'${value.replaceAll("'", `"'"'`)}'`;
}

export function managedBlock(client: Client, vars: Record<string, string>, extraLines: string[] = []): string {
  const lines = Object.entries(vars).map(([key, value]) => `export ${key}=${shellQuote(value)}`);
  return [`# LLMAPI:BEGIN ${client}`, ...lines, ...extraLines, `# LLMAPI:END ${client}`].join('\n');
}

export function applyManagedBlock(profile: string, client: Client, block: string): string {
  const cleaned = removeManagedBlock(profile, client);
  const separator = cleaned.length > 0 && !cleaned.endsWith('\n') ? '\n' : '';
  return `${cleaned}${separator}${block}\n`;
}

export function removeManagedBlock(profile: string, client: Client): string {
  const begin = `# LLMAPI:BEGIN ${client}`;
  const end = `# LLMAPI:END ${client}`;
  let output = profile;
  while (true) {
    const start = output.indexOf(begin);
    if (start < 0) return output;
    const finish = output.indexOf(end, start);
    if (finish < 0) {
      throw new Error(`managed LLMAPI block for ${client} is incomplete`);
    }
    let after = finish + end.length;
    if (output[after] === '\r') after++;
    if (output[after] === '\n') after++;
    output = `${output.slice(0, start)}${output.slice(after)}`;
  }
}

function removeCodexManagedConfig(config: string): string {
  let output = removeManagedBlock(config, 'codex');
  const lines = output.split(/(?<=\n)/);
  const kept: string[] = [];
  let skippingProvider = false;
  for (const line of lines) {
    if (/^\s*\[model_providers\.llmapi\]\s*(?:#.*)?(?:\r?\n)?$/.test(line)) {
      skippingProvider = true;
      continue;
    }
    if (skippingProvider && /^\s*\[/.test(line)) {
      skippingProvider = false;
    }
    if (!skippingProvider) kept.push(line);
  }
  output = kept.join('');
  return output;
}

function appendNewline(value: string): string {
  return value.length > 0 && !value.endsWith('\n') ? `${value}\n` : value;
}

export function applyCodexConfig(config: string, model: string, serverUrl: string): string {
  const cleaned = removeCodexManagedConfig(config);
  const firstTable = cleaned.search(/^\s*\[/m);
  const root = firstTable < 0 ? cleaned : cleaned.slice(0, firstTable);
  const tables = firstTable < 0 ? '' : cleaned.slice(firstTable);
  const selection = [
    '# LLMAPI:BEGIN codex',
    `model = ${JSON.stringify(model)}`,
    'model_provider = "llmapi"',
    '# LLMAPI:END codex',
    '',
  ].join('\n');
  const provider = [
    '[model_providers.llmapi]',
    'name = "LLMAPI"',
    `base_url = ${JSON.stringify(`${serverUrl}/v1`)}`,
    'wire_api = "responses"',
    'env_key = "LLMAPI_TOKEN"',
    '',
  ].join('\n');
  const beforeTables = `${appendNewline(root)}${selection}`;
  const body = `${beforeTables}${tables}`;
  return `${appendNewline(body)}${provider}`;
}

export function backupPath(home: string, hash: string): string {
  return path.join(home, BACKUP_DIR, hash.slice(0, 2), hash.slice(2));
}

export function storeBackup(home: string, content: string | null): string {
  const data = content ?? '';
  const hash = sha256(data);
  const target = backupPath(home, hash);
  ensurePrivateDir(path.dirname(target));
  if (!fs.existsSync(target)) atomicWrite(target, data, 0o600);
  return target;
}

export function saveReceipt(receipt: ClientReceipt): string {
  const receiptDir = path.join(receipt.home, RECEIPT_DIR);
  ensurePrivateDir(receiptDir);
  const name = `${receipt.client}-${Date.now()}-${crypto.randomBytes(4).toString('hex')}.json`;
  const filePath = path.join(receiptDir, name);
  atomicWrite(filePath, JSON.stringify(receipt, null, 2), 0o600);
  return filePath;
}

export function detectLegacyState(home: string): boolean {
  const legacy = path.join(home, LEGACY_STATE_ROOT);
  return fs.existsSync(path.join(legacy, 'receipts')) || fs.existsSync(path.join(legacy, 'backups'));
}

export function detectShellProfile(home: string, shell?: string): string {
  const shellPath = shell ?? process.env.SHELL ?? '';
  return path.join(home, shellPath.includes('bash') ? '.bashrc' : '.zshrc');
}

function receiptFile(pathname: string, original: string | null, configured: string, home: string): ClientReceiptFile {
  return {
    path: pathname,
    originalHash: original === null ? null : sha256(original),
    configuredHash: sha256(configured),
    backupPath: storeBackup(home, original),
  };
}

export async function configureCodex(options: ConfigureCodexOptions): Promise<ClientReceipt> {
  const home = path.resolve(options.home);
  const normalizedUrl = normalizeServerUrl(options.serverUrl);
  const configPath = path.join(home, '.codex', 'config.toml');
  ensurePrivateDir(path.dirname(configPath));
  const original = readOwnedFile(configPath);
  const configured = applyCodexConfig(original ?? '', options.model, normalizedUrl);
  atomicWrite(configPath, configured, 0o600);

  const receipt: ClientReceipt = {
    version: 1,
    client: 'codex',
    home,
    serverUrl: normalizedUrl,
    files: [receiptFile(configPath, original, configured, home)],
    shellProfile: null,
    envVars: ['LLMAPI_TOKEN'],
    legacyStateDetected: detectLegacyState(home),
  };
  saveReceipt(receipt);
  return receipt;
}

export async function configureClaude(options: ConfigureClaudeOptions): Promise<ClientReceipt> {
  const home = path.resolve(options.home);
  const normalizedUrl = normalizeServerUrl(options.serverUrl);
  const shellProfile = detectShellProfile(home, options.shell);
  const original = readOwnedFile(shellProfile);
  const block = managedBlock(
    'claude',
    { ANTHROPIC_BASE_URL: normalizedUrl },
    ['export ANTHROPIC_API_KEY="${LLMAPI_TOKEN}"']
  );
  const configured = applyManagedBlock(original ?? '', 'claude', block);
  atomicWrite(shellProfile, configured, 0o600);

  const receipt: ClientReceipt = {
    version: 1,
    client: 'claude',
    home,
    serverUrl: normalizedUrl,
    files: [receiptFile(shellProfile, original, configured, home)],
    shellProfile,
    envVars: ['LLMAPI_TOKEN', 'ANTHROPIC_BASE_URL', 'ANTHROPIC_API_KEY'],
    legacyStateDetected: detectLegacyState(home),
  };
  saveReceipt(receipt);
  return receipt;
}

function receiptDirectory(home: string): string {
  return path.join(home, RECEIPT_DIR);
}

function receiptNames(directory: string): string[] {
  return fs.readdirSync(directory)
    .filter((name) => {
      if (!name.endsWith('.json')) return false;
      return fs.lstatSync(path.join(directory, name)).isFile();
    })
    .sort((left, right) => {
      const difference = fs.statSync(path.join(directory, right)).mtimeMs - fs.statSync(path.join(directory, left)).mtimeMs;
      return difference || right.localeCompare(left);
    });
}

function isWithin(base: string, target: string): boolean {
  const relative = path.relative(base, target);
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative));
}

function validateReceipt(receipt: ClientReceipt, home: string): void {
  const resolvedHome = path.resolve(home);
  if (receipt.version !== 1 || !['codex', 'claude'].includes(receipt.client) || path.resolve(receipt.home) !== resolvedHome) {
    throw new Error('receipt is invalid for this home directory');
  }
  for (const entry of receipt.files) {
    if (!isWithin(resolvedHome, entry.path) || !isWithin(path.join(resolvedHome, BACKUP_DIR), entry.backupPath)) {
      throw new Error('receipt contains an unsafe file path');
    }
  }
}

export function loadReceipt(home: string, requested?: string): ClientReceipt {
  const directory = receiptDirectory(home);
  if (!fs.existsSync(directory)) throw new Error('no LLMAPI receipts found');
  const names = receiptNames(directory);
  const name = requested ? path.basename(requested).replace(/\.json$/, '') + '.json' : names[0];
  if (!name || name !== path.basename(name) || !names.includes(name)) {
    throw new Error('requested receipt was not found');
  }
  const receiptPath = path.join(directory, name);
  assertOwnership(receiptPath);
  let receipt: ClientReceipt;
  try {
    receipt = JSON.parse(fs.readFileSync(receiptPath, 'utf8')) as ClientReceipt;
  } catch {
    throw new Error('receipt is not valid JSON');
  }
  validateReceipt(receipt, home);
  return receipt;
}

export function loadLatestReceiptForClient(home: string, client: Client): ClientReceipt {
  const directory = receiptDirectory(home);
  if (!fs.existsSync(directory)) throw new Error(`no LLMAPI receipt found for ${client}`);
  const names = receiptNames(directory);
  for (const name of names) {
    try {
      const receipt = loadReceipt(home, name);
      if (receipt.client === client) return receipt;
    } catch {
      // Ignore unrelated invalid receipts; a valid receipt is required to modify any file.
    }
  }
  throw new Error(`no LLMAPI receipt found for ${client}`);
}

export async function restoreClient(receipt: ClientReceipt): Promise<void> {
  validateReceipt(receipt, receipt.home);
  for (const entry of receipt.files) {
    assertOwnership(entry.path);
    const current = readOwnedFile(entry.path);
    if (current === null || sha256(current) !== entry.configuredHash) {
      throw new Error(`refusing to restore ${displayPath(entry.path)}: file has changed since configuration`);
    }
    if (!fs.existsSync(entry.backupPath)) {
      throw new Error(`backup missing for ${displayPath(entry.path)}; cannot restore`);
    }
    const backup = fs.readFileSync(entry.backupPath, 'utf8');
    if (entry.originalHash === null) {
      fs.unlinkSync(entry.path);
    } else {
      atomicWrite(entry.path, backup, 0o600);
    }
  }
}

export async function uninstallClient(home: string, client: Client): Promise<ClientReceipt> {
  home = path.resolve(home);
  const source = loadLatestReceiptForClient(home, client);
  const changes: ClientReceiptFile[] = [];
  for (const entry of source.files) {
    assertOwnership(entry.path);
    const current = readOwnedFile(entry.path);
    if (current === null || sha256(current) !== entry.configuredHash) {
      throw new Error(`refusing to uninstall ${displayPath(entry.path)}: file has changed since configuration`);
    }
    const configured = client === 'codex' ? removeCodexManagedConfig(current) : removeManagedBlock(current, 'claude');
    if (configured !== current) {
      atomicWrite(entry.path, configured, 0o600);
      changes.push(receiptFile(entry.path, current, configured, home));
    }
  }
  if (changes.length === 0) throw new Error(`no managed LLMAPI configuration found for ${client}`);

  const receipt: ClientReceipt = {
    version: 1,
    client,
    home,
    serverUrl: source.serverUrl,
    files: changes,
    shellProfile: client === 'claude' ? changes[0].path : null,
    envVars: source.envVars,
    legacyStateDetected: detectLegacyState(home),
  };
  saveReceipt(receipt);
  return receipt;
}

function displayPath(value: string): string {
  return value.replace(/(?:sk|tok|key)[_-]?[A-Za-z0-9_-]{8,}|[A-Za-z0-9_-]{32,}/gi, '[REDACTED]');
}
