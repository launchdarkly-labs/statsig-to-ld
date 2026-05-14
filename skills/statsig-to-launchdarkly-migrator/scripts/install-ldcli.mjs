#!/usr/bin/env node
// Detect or install the LaunchDarkly CLI (`ldcli`). Never logs in — login
// must happen interactively in the user's own terminal so the token does not
// pass through Claude's transcript.

import { execFileSync, spawnSync } from 'node:child_process';
import { platform } from 'node:os';

function has(cmd) {
  const r = spawnSync('which', [cmd], { encoding: 'utf8' });
  return r.status === 0 && r.stdout.trim().length > 0;
}

if (has('ldcli')) {
  const version = execFileSync('ldcli', ['--version'], { encoding: 'utf8' }).trim();
  console.log(`ldcli already installed: ${version}`);
  process.exit(0);
}

console.log('ldcli not found on PATH. Attempting install…');

const os = platform();

if (os === 'darwin' && has('brew')) {
  console.log('Using Homebrew tap launchdarkly/ldcli');
  const r = spawnSync('brew', ['install', 'launchdarkly/tap/ldcli'], { stdio: 'inherit' });
  if (r.status === 0 && has('ldcli')) {
    console.log('Installed via brew.');
    process.exit(0);
  }
  console.error('brew install failed; falling through to curl installer');
}

console.log('');
console.log('Run this in your own terminal to install ldcli:');
console.log('');
console.log('  curl -fsSL https://launchdarkly.github.io/ldcli/install.sh | sh');
console.log('');
console.log('Then re-run the migration. Do not run `ldcli login` from inside Claude — run it yourself.');
process.exit(1);
