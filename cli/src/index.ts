#!/usr/bin/env node
import { Command } from 'commander';
import { initCommand } from './init.js';
import { statusCommand } from './status.js';
import { doctorCommand } from './doctor.js';

const program = new Command();

program
  .name('codex-gateway')
  .description('Configure Codex CLI / Claude Code to use Codex Gateway')
  .version('1.0.0');

program.addCommand(initCommand);
program.addCommand(statusCommand);
program.addCommand(doctorCommand);

program.parse();
