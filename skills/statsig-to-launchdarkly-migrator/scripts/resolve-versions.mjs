#!/usr/bin/env node
// Resolve the current published version of every LaunchDarkly package the
// migration touches. Writes migration-versions.json to the project root.
//
// Why this script exists: agentic coding tools — including Claude — routinely
// emit stale LaunchDarkly Node SDK versions pulled from training data. The
// most common stale artifact is `launchdarkly-node-server-sdk@5.x`, which has
// since been renamed to `@launchdarkly/node-server-sdk`. Doing a live `npm view`
// lookup at migration time is the only reliable defense.

import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

const PACKAGES = [
  'launchdarkly-js-client-sdk',
  'launchdarkly-react-client-sdk',
  '@launchdarkly/node-server-sdk',
  '@launchdarkly/observability',
  '@launchdarkly/session-replay',
];

// Legacy names to flag if they ever appear in migrated code.
const LEGACY_NAMES = [
  'launchdarkly-node-server-sdk', // renamed to @launchdarkly/node-server-sdk
];

function npmView(pkg) {
  try {
    const out = execFileSync('npm', ['view', pkg, 'version'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 15_000,
    });
    return out.trim();
  } catch (err) {
    return null;
  }
}

const resolved = {};
const failed = [];

for (const pkg of PACKAGES) {
  const version = npmView(pkg);
  if (version) {
    resolved[pkg] = version;
  } else {
    failed.push(pkg);
  }
}

const out = {
  resolved_at: new Date().toISOString(),
  packages: resolved,
  failed_lookups: failed,
  legacy_names_to_reject: LEGACY_NAMES,
  fallback_floors_doc: 'skills/statsig-to-launchdarkly-migrator/references/version-floors.md',
};

const dest = resolve(process.cwd(), 'migration-versions.json');
writeFileSync(dest, JSON.stringify(out, null, 2) + '\n');

if (failed.length === PACKAGES.length) {
  console.error('All npm lookups failed. Falling back to version floors. See', out.fallback_floors_doc);
  process.exit(2);
}

console.log('Resolved LaunchDarkly SDK versions:');
for (const [pkg, version] of Object.entries(resolved)) {
  console.log(`  ${pkg.padEnd(40)} ${version}`);
}
if (failed.length) {
  console.error('\nFailed lookups (will need fallback):');
  for (const pkg of failed) console.error(`  ${pkg}`);
}
console.log(`\nWrote ${dest}`);
