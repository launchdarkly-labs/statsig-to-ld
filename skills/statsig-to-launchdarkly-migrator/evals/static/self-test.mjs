#!/usr/bin/env node
// Meta-test: confirm the assertion library does what it claims.
//   - Every "expected" fixture passes every assertion.
//   - Every "bad" fixture is caught by at least one assertion.
//
// Run: node evals/static/self-test.mjs
// Exits 0 on success, 1 on any divergence.

import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

function run(input) {
  const r = spawnSync('node', [join(__dirname, 'run.mjs'), '--input', input, '--json'], {
    encoding: 'utf8',
  });
  if (r.status !== 0 && r.status !== 1) {
    console.error('runner crashed', r.stderr);
    process.exit(2);
  }
  return JSON.parse(r.stdout);
}

const expected = run(join(__dirname, 'fixtures', 'expected'));
const bad = run(join(__dirname, 'fixtures', 'bad'));

let problems = 0;

if (expected.totals.fail !== 0) {
  problems++;
  console.error(`EXPECTED fixtures should have 0 failures, got ${expected.totals.fail}`);
  for (const r of expected.results) {
    for (const a of r.assertions) {
      if (!a.ok) console.error(`  ${r.file} → ${a.name}: ${a.detail}`);
    }
  }
}

for (const r of bad.results) {
  const anyFail = r.assertions.some((a) => !a.ok);
  if (!anyFail) {
    problems++;
    console.error(`BAD fixture ${r.file} was not caught by any assertion`);
  }
}

if (problems === 0) {
  console.log('self-test ok');
  console.log(`  expected: ${expected.totals.pass} pass / ${expected.totals.fail} fail across ${expected.totals.files} files`);
  console.log(`  bad:      ${bad.totals.pass} pass / ${bad.totals.fail} fail across ${bad.totals.files} files`);
  process.exit(0);
}

process.exit(1);
