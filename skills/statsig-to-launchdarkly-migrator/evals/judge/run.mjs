#!/usr/bin/env node
// LLM-as-judge eval runner. Scores migrated code against rubric.md across
// five dimensions, each 1–5. Pass threshold: every dimension ≥ 4.
//
// Defaults to the bundled "expected" fixtures so the harness can self-check.
// In a real flow, point --input at the migration output:
//
//   node evals/judge/run.mjs --input ./
//
// Requires the `claude` CLI (Claude Code) on PATH. Each fixture is one
// non-interactive call. Cost scales linearly with fixture count — small.

import { readFileSync, readdirSync, statSync, existsSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { dirname, join, resolve, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

const args = process.argv.slice(2);
let inputDir = join(__dirname, '..', 'static', 'fixtures', 'expected');
let model = 'claude-sonnet-4-6';
let outPath = null;
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--input') inputDir = resolve(args[++i]);
  else if (args[i] === '--model') model = args[++i];
  else if (args[i] === '--out') outPath = resolve(args[++i]);
}

const SOURCE_EXTS = new Set(['.js', '.jsx', '.ts', '.tsx', '.mjs', '.cjs']);
function walk(dir, files = []) {
  if (!existsSync(dir)) return files;
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry.startsWith('.')) continue;
    const p = join(dir, entry);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, files);
    else if (SOURCE_EXTS.has(p.slice(p.lastIndexOf('.')))) files.push(p);
  }
  return files;
}

const rubric = readFileSync(join(__dirname, 'rubric.md'), 'utf8');
const files = walk(inputDir);

if (!files.length) {
  console.error(`No source files in ${inputDir}`);
  process.exit(2);
}

function ensureClaudeAvailable() {
  const r = spawnSync('claude', ['--version'], { encoding: 'utf8' });
  if (r.status !== 0) {
    console.error('claude CLI not found on PATH. Install: https://github.com/anthropics/claude-code');
    process.exit(3);
  }
}
ensureClaudeAvailable();

function scoreFile(filePath, src) {
  const prompt = [
    'You are an expert reviewer of LaunchDarkly migrations.',
    'Score the following migrated file against the rubric below.',
    'Return ONLY a single JSON object matching the rubric output format. No prose.',
    '',
    '=== RUBRIC ===',
    rubric,
    '',
    '=== FILE: ' + filePath + ' ===',
    src,
  ].join('\n');

  const r = spawnSync(
    'claude',
    ['-p', prompt, '--model', model, '--output-format', 'json'],
    { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 },
  );

  if (r.status !== 0) {
    return { error: r.stderr || 'claude CLI failed', raw: r.stdout };
  }

  // `claude -p --output-format json` returns a JSON envelope with the
  // assistant message under .result (CLI version-dependent — fall back to
  // raw stdout if needed).
  let envelope;
  try {
    envelope = JSON.parse(r.stdout);
  } catch {
    return { error: 'failed to parse claude envelope', raw: r.stdout };
  }
  const text =
    envelope.result ??
    envelope.response ??
    envelope.text ??
    r.stdout;

  const jsonMatch = String(text).match(/\{[\s\S]*\}/);
  if (!jsonMatch) {
    return { error: 'no JSON in judge response', raw: text };
  }
  try {
    return { score: JSON.parse(jsonMatch[0]) };
  } catch {
    return { error: 'judge JSON did not parse', raw: jsonMatch[0] };
  }
}

const results = [];
for (const file of files) {
  const src = readFileSync(file, 'utf8');
  const rel = relative(inputDir, file);
  process.stderr.write(`scoring ${rel}…\n`);
  const r = scoreFile(rel, src);
  results.push({ file: rel, ...r });
}

const passes = results.filter((r) => r.score?.passes === true).length;
const out = {
  input: inputDir,
  model,
  pass_rate: results.length ? passes / results.length : 0,
  fixtures: results,
};

const json = JSON.stringify(out, null, 2);
if (outPath) {
  writeFileSync(outPath, json + '\n');
  console.log(`wrote ${outPath}`);
} else {
  console.log(json);
}

process.exit(passes === results.length ? 0 : 1);
