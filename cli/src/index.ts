#!/usr/bin/env node
import { Command } from 'commander';
import * as fs from 'node:fs';
import { pathToFileURL } from 'node:url';
import { fileURLToPath } from 'node:url';

import { doctorCommand } from './doctor.js';
import { initCommand } from './init.js';
import { restoreCommand } from './restore.js';
import { statusCommand } from './status.js';
import { uninstallCommand } from './uninstall.js';

export function createProgram(): Command {
  const program = new Command();
  program
    .name('llmrouter')
    .description('Configure supported clients to use LLMROUTER')
    .version('1.0.0');
  program.addCommand(initCommand);
  program.addCommand(statusCommand);
  program.addCommand(doctorCommand);
  program.addCommand(restoreCommand);
  program.addCommand(uninstallCommand);
  return program;
}

export function isMainModule(moduleUrl: string, entryPath = process.argv[1]): boolean {
  if (!entryPath) return false;
  try {
    return fs.realpathSync(fileURLToPath(moduleUrl)) === fs.realpathSync(entryPath);
  } catch {
    return moduleUrl === pathToFileURL(entryPath).href;
  }
}

if (isMainModule(import.meta.url)) {
  createProgram().parse();
}
