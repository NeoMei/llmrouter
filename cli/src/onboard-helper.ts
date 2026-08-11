import * as fs from 'node:fs';
import * as path from 'node:path';
import { execFileSync } from 'node:child_process';

function unsafe(): Error { return new Error('unsafe onboarding helper'); }

function verifyNode(file: string, executable = false): void {
  const stat = fs.lstatSync(file);
  if (stat.isSymbolicLink() || stat.uid !== process.getuid?.() || (stat.mode & 0o022) !== 0) throw unsafe();
  if (executable && (!stat.isFile() || (stat.mode & 0o100) === 0)) throw unsafe();
}

export function resolveOnboardHelper(home: string): string {
  const canonicalHome = fs.realpathSync(home);
  verifyNode(canonicalHome);
  const state = path.join(canonicalHome, '.llmapi');
  const bin = path.join(state, 'bin');
  verifyNode(state);
  verifyNode(bin);
  const helper = path.join(bin, process.platform === 'win32' ? 'llmapi-onboard.exe' : 'llmapi-onboard');
  verifyNode(helper, true);
  const canonicalHelper = fs.realpathSync(helper);
  if (canonicalHelper !== helper || !canonicalHelper.startsWith(`${bin}${path.sep}`)) throw unsafe();
  return canonicalHelper;
}

export function helperArgs(home: string, command: string, args: readonly string[] = []): string[] {
  return ['--home', path.resolve(home), command, ...args];
}

export function runOnboard(home: string, command: string, args: readonly string[] = []): void {
  const helper = resolveOnboardHelper(home);
  execFileSync(helper, helperArgs(home, command, args), { stdio: 'inherit', env: process.env });
}

export function safeError(error: unknown): string {
  const message = error instanceof Error ? error.message : 'onboarding helper failed';
  return message.replace(/(?:sk|tok|key)[_-]?[A-Za-z0-9_-]{8,}|[A-Za-z0-9_-]{32,}/gi, '[REDACTED]');
}
