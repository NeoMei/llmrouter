import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { execFileSync } from 'node:child_process';

const MAX_OUTPUT = 64 * 1024;
function unsafe(): Error { return new Error('unsafe onboarding helper'); }
function verifyNode(file: string, executable = false): fs.Stats {
  const stat = fs.lstatSync(file);
  if (stat.isSymbolicLink() || (process.platform !== 'win32' && (stat.uid !== process.getuid?.() || (stat.mode & 0o022) !== 0))) throw unsafe();
  if (process.platform !== 'win32' && stat.nlink !== 1) throw unsafe();
  if (executable && process.platform !== 'win32' && (!stat.isFile() || (stat.mode & 0o100) === 0)) throw unsafe();
  return stat;
}
function digest(file: string): string { return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }
export type VerifiedHelper = { path: string; checksum: string };
export function resolveOnboardHelper(home: string): VerifiedHelper {
  if (process.platform === 'win32') throw new Error('trusted helper validation is unavailable on Windows');
  const canonicalHome = fs.realpathSync(home); verifyNode(canonicalHome);
  const state = path.join(canonicalHome, '.llmapi'); const bin = path.join(state, 'bin'); const checksums = path.join(state, 'checksums');
  verifyNode(state); verifyNode(bin); verifyNode(checksums);
  const helper = path.join(bin, 'llmapi-onboard'); const checksumFile = path.join(checksums, 'llmapi-onboard.sha256');
  verifyNode(helper, true); verifyNode(checksumFile);
  const canonicalHelper = fs.realpathSync(helper); if (canonicalHelper !== helper || !canonicalHelper.startsWith(`${bin}${path.sep}`)) throw unsafe();
  const expected = fs.readFileSync(checksumFile, 'utf8').trim().toLowerCase();
  if (!/^[a-f0-9]{64}$/.test(expected) || digest(helper) !== expected) throw unsafe();
  return { path: helper, checksum: expected };
}
export function helperArgs(home: string, command: string, args: readonly string[] = []): string[] { return ['--home', path.resolve(home), command, ...args]; }
export function redact(value: string, token = process.env.LLMAPI_TOKEN ?? ''): string {
  return value.slice(0, MAX_OUTPUT).replaceAll(token, token ? '[REDACTED]' : '').replace(/(?:sk|tok|key)[_-]?[A-Za-z0-9_-]{8,}|Bearer\s+\S+/gi, '[REDACTED]');
}
export function runOnboard(home: string, command: string, args: readonly string[] = []): void {
  const verified = resolveOnboardHelper(home); const copy = path.join(os.tmpdir(), `llmapi-onboard-${process.pid}-${crypto.randomBytes(8).toString('hex')}`);
  try {
    const bytes = fs.readFileSync(verified.path); if (crypto.createHash('sha256').update(bytes).digest('hex') !== verified.checksum) throw unsafe();
    fs.writeFileSync(copy, bytes, { mode: 0o700, flag: 'wx' }); fs.chmodSync(copy, 0o700);
    const output = execFileSync(copy, helperArgs(home, command, args), { encoding: 'utf8', maxBuffer: MAX_OUTPUT, env: process.env, stdio: ['ignore', 'pipe', 'pipe'] });
    if (output) process.stdout.write(redact(output));
  } catch (error: any) {
    const output = `${error?.stdout ?? ''}${error?.stderr ?? ''}`;
    throw new Error(redact(output || 'trusted onboarding helper failed'));
  } finally { try { fs.unlinkSync(copy); } catch {} }
}
export function safeError(error: unknown): string { return redact(error instanceof Error ? error.message : 'onboarding helper failed'); }
