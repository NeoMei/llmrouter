/**
 * Claude Code reads ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY from the environment.
 * We print instructions rather than silently overwriting — users may have a
 * direct Anthropic key they want to preserve.
 */
export function writeClaudeConfig(serverUrl: string, apiToken: string): void {
  console.log('  Add the following to your shell profile (~/.zshrc or ~/.bashrc):\n');
  console.log(`    export ANTHROPIC_BASE_URL="${serverUrl}"`);
  console.log(`    export ANTHROPIC_API_KEY="${apiToken}"\n`);
  console.log('  Then restart your shell or run: source ~/.zshrc');
}
