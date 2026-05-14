#!/usr/bin/env node
// Run the static eval assertions against a directory of migrated code.
//
// Usage:
//   node evals/static/run.mjs                          # runs against bundled fixtures
//   node evals/static/run.mjs --input <dir>            # runs against a real migration output
//   node evals/static/run.mjs --input <dir> --json     # machine-readable output

import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { resolve, join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  SOURCE_ASSERTIONS,
  packageJsonUsesCurrentLDVersions,
} from './assertions.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const args = process.argv.slice(2);
let inputDir = join(__dirname, 'fixtures', 'expected');
let jsonOut = false;
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--input') inputDir = resolve(args[++i]);
  else if (args[i] === '--json') jsonOut = true;
}

const SOURCE_EXTS = new Set(['.js', '.jsx', '.ts', '.tsx', '.mjs', '.cjs']);

function walk(dir, files = []) {
  if (!existsSync(dir)) return files;
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry.startsWith('.')) continue;
    const p = join(dir, entry);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, files);
    else files.push(p);
  }
  return files;
}

const allFiles = walk(inputDir);
const sourceFiles = allFiles.filter((f) =>
  SOURCE_EXTS.has(f.slice(f.lastIndexOf('.'))),
);
const pkgJsonFile = allFiles.find((f) => f.endsWith('package.json'));
const versionsFile = allFiles.find((f) => f.endsWith('migration-versions.json'));

let resolvedVersions = null;
if (versionsFile) {
  try {
    resolvedVersions = JSON.parse(readFileSync(versionsFile, 'utf8')).packages;
  } catch {}
}

const results = [];

for (const file of sourceFiles) {
  const src = readFileSync(file, 'utf8');
  const fileResults = [];
  for (const a of SOURCE_ASSERTIONS) {
    const r = a(src, file);
    fileResults.push(r);
  }
  results.push({
    file: relative(inputDir, file),
    assertions: fileResults,
  });
}

if (pkgJsonFile) {
  const pkg = JSON.parse(readFileSync(pkgJsonFile, 'utf8'));
  const r = packageJsonUsesCurrentLDVersions(pkg, resolvedVersions);
  results.push({
    file: relative(inputDir, pkgJsonFile),
    assertions: [r],
  });
}

const totals = { files: results.length, pass: 0, fail: 0 };
for (const r of results) {
  for (const a of r.assertions) {
    if (a.ok) totals.pass++;
    else totals.fail++;
  }
}

if (jsonOut) {
  process.stdout.write(JSON.stringify({ totals, results, inputDir }, null, 2) + '\n');
} else {
  console.log(`Static evals — input: ${inputDir}`);
  console.log(`Files scanned: ${totals.files}, assertions passed: ${totals.pass}, failed: ${totals.fail}`);
  console.log('');
  for (const r of results) {
    const failed = r.assertions.filter((a) => !a.ok);
    if (failed.length === 0) {
      console.log(`  PASS  ${r.file}`);
    } else {
      console.log(`  FAIL  ${r.file}`);
      for (const a of failed) console.log(`         ↳ ${a.name}: ${a.detail}`);
    }
  }
}

process.exit(totals.fail === 0 ? 0 : 1);
