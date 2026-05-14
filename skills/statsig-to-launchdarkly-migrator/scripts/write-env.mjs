#!/usr/bin/env node
// Write a LaunchDarkly key to .env without it transiting Claude's transcript.
//
// Usage:
//   ldcli environments get --project P --environment test --output json \
//     | jq -r '.apiKey' \
//     | node scripts/write-env.mjs --var LD_CLIENT_SIDE_ID
//
// Refuses to write if .env is not in .gitignore.
// Also updates .env.example with the variable NAME only (never the value).

import { readFileSync, writeFileSync, existsSync, appendFileSync } from 'node:fs';
import { resolve } from 'node:path';

const args = process.argv.slice(2);
let varName = null;
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--var') varName = args[++i];
}

if (!varName) {
  console.error('Usage: write-env.mjs --var <NAME>   (key piped on stdin)');
  process.exit(2);
}

if (!/^[A-Z][A-Z0-9_]*$/.test(varName)) {
  console.error(`Refusing to write env var ${JSON.stringify(varName)} — must be SCREAMING_SNAKE_CASE.`);
  process.exit(2);
}

const cwd = process.cwd();
const gitignorePath = resolve(cwd, '.gitignore');
if (!existsSync(gitignorePath)) {
  console.error('No .gitignore at project root. Refusing to write .env. Create .gitignore first.');
  process.exit(3);
}
const gitignore = readFileSync(gitignorePath, 'utf8');
if (!gitignoreProtectsDotEnv(gitignore)) {
  console.error('.gitignore does not exclude the bare `.env` file. Refusing to write the key.');
  console.error('Patterns like `.env.local` do NOT ignore `.env` — they only ignore `.env.local`.');
  console.error('Add `.env` (or `.env*`) on its own line to .gitignore and re-run.');
  process.exit(3);
}

// Does this .gitignore include a pattern that would actually ignore the
// literal file `.env`? Patterns like `.env.local` or `.env.*.local` do NOT
// — they match different filenames. Only patterns whose glob would match
// the basename `.env` count.
function gitignoreProtectsDotEnv(contents) {
  // Walk top-to-bottom matching gitignore semantics: a later `!pattern`
  // un-ignores files that an earlier pattern ignored. Track the running
  // ignore-state for the bare `.env` basename.
  let ignored = false;
  for (const raw of contents.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const isNegation = line.startsWith('!');
    const pattern = (isNegation ? line.slice(1) : line)
      .replace(/^\/+/, '')
      .replace(/\/+$/, '');
    if (matchesDotEnv(pattern)) {
      ignored = !isNegation;
    }
  }
  return ignored;
}

function matchesDotEnv(pattern) {
  // Exact basename match.
  if (pattern === '.env') return true;
  // **/X — applies anywhere.
  if (pattern.startsWith('**/')) return matchesDotEnv(pattern.slice(3));
  // Translate the subset of gitignore globs we accept into a regex over
  // the basename `.env`. Only allow `*` and `?` wildcards — anything more
  // exotic (character classes, mid-pattern `/`) makes us err on the side
  // of refusing.
  if (/[\\\[\]\{\}\/]/.test(pattern)) return false;
  let re = '^';
  for (const ch of pattern) {
    if (ch === '*') re += '[^/]*';
    else if (ch === '?') re += '[^/]';
    else re += ch.replace(/[.+^$()|]/g, '\\$&');
  }
  re += '$';
  return new RegExp(re).test('.env');
}

// Read key from stdin without ever logging it.
let key = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => { key += chunk; });
process.stdin.on('end', () => {
  key = key.trim();
  if (!key) {
    console.error('No key received on stdin. Pipe the key in.');
    process.exit(2);
  }
  if (/\s/.test(key)) {
    console.error('Key contains whitespace. Refusing.');
    process.exit(2);
  }
  if (key.startsWith('client-') && varName !== 'STATSIG_CLIENT_KEY') {
    console.error('Refusing to write a value that starts with `client-` to a LaunchDarkly env var.');
    console.error('That looks like a Statsig SDK key. LaunchDarkly Client-Side IDs do not have that prefix.');
    process.exit(4);
  }

  const envPath = resolve(cwd, '.env');
  let envContents = existsSync(envPath) ? readFileSync(envPath, 'utf8') : '';
  const line = `${varName}=${key}\n`;
  const re = new RegExp(`^${varName}=.*$`, 'm');
  if (re.test(envContents)) {
    envContents = envContents.replace(re, line.trimEnd());
  } else {
    if (envContents.length && !envContents.endsWith('\n')) envContents += '\n';
    envContents += line;
  }
  writeFileSync(envPath, envContents);

  // .env.example: variable name only.
  const examplePath = resolve(cwd, '.env.example');
  const placeholder = `${varName}=\n`;
  let exampleContents = existsSync(examplePath) ? readFileSync(examplePath, 'utf8') : '';
  if (!new RegExp(`^${varName}=`, 'm').test(exampleContents)) {
    if (exampleContents.length && !exampleContents.endsWith('\n')) exampleContents += '\n';
    exampleContents += placeholder;
    writeFileSync(examplePath, exampleContents);
  }

  // NEVER echo the key itself.
  console.log(`Wrote ${varName} to .env (${envPath}).`);
  console.log(`Updated .env.example with ${varName}= (value blank).`);
});
