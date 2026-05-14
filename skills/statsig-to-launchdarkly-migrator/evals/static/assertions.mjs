// Pure assertion functions for migrated LaunchDarkly code. Each assertion
// takes the file contents (and path) and returns { ok, name, detail }.
//
// These are the rules the skill must produce code that satisfies. The
// runner orchestrates calling them across a directory tree.

const STATSIG_PACKAGES = [
  'statsig-js',
  '@statsig/react-bindings',
  '@statsig/web-analytics',
  '@statsig/session-replay',
  'statsig-node',
];

const LEGACY_LD_NAMES = [
  // Old unscoped Node SDK package — agentic migrations pull this from
  // stale training data. The current package is @launchdarkly/node-server-sdk.
  'launchdarkly-node-server-sdk',
  'ldclient-node',
];

const LD_PACKAGES = [
  'launchdarkly-js-client-sdk',
  'launchdarkly-react-client-sdk',
  '@launchdarkly/node-server-sdk',
  '@launchdarkly/observability',
  '@launchdarkly/session-replay',
];

function importsPackage(src, name) {
  // Catch both `from 'name'` and `require('name')` shapes.
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const fromRe = new RegExp(`from\\s+['"]${escaped}['"]`);
  const requireRe = new RegExp(`require\\(\\s*['"]${escaped}['"]\\s*\\)`);
  return fromRe.test(src) || requireRe.test(src);
}

export function noLegacyLDPackageNames(src) {
  for (const legacy of LEGACY_LD_NAMES) {
    if (importsPackage(src, legacy)) {
      return {
        ok: false,
        name: 'noLegacyLDPackageNames',
        detail: `Found legacy LaunchDarkly package name "${legacy}". Use the scoped @launchdarkly/* equivalent. This is the #1 stale-training-data failure mode.`,
      };
    }
  }
  return { ok: true, name: 'noLegacyLDPackageNames' };
}

export function noStatsigImportsUnlessExperimentsPresent(src) {
  const importsStatsig = STATSIG_PACKAGES.some((p) => importsPackage(src, p));
  const usesExperiment =
    /\b(getExperiment|useExperiment|useLayer)\s*\(/.test(src);
  if (importsStatsig && !usesExperiment) {
    return {
      ok: false,
      name: 'noStatsigImportsUnlessExperimentsPresent',
      detail: 'File still imports Statsig but uses no experiment API. Migration should have removed the Statsig import.',
    };
  }
  return { ok: true, name: 'noStatsigImportsUnlessExperimentsPresent' };
}

export function noLiteralStatsigKey(src) {
  // Statsig keys look like client-XXX… Anything matching survives means a
  // key got copy-pasted into migrated code.
  const m = src.match(/['"`]client-[a-zA-Z0-9_-]{8,}['"`]/);
  if (m) {
    return {
      ok: false,
      name: 'noLiteralStatsigKey',
      detail: `Found a Statsig-shaped key literal in migrated code: ${m[0].slice(0, 16)}…`,
    };
  }
  return { ok: true, name: 'noLiteralStatsigKey' };
}

export function noLiteralLDKey(src) {
  // LD Client-Side IDs and SDK Keys are hex-ish blobs. Catch obvious
  // hardcoded uses by looking for `initialize('xxxxxxxx...'` literals
  // that aren't a placeholder.
  const initRe = /\binitialize\(\s*['"]([^'"]+)['"]/g;
  for (const m of src.matchAll(initRe)) {
    const v = m[1];
    if (
      /^[a-f0-9]{16,}$/i.test(v) ||
      /^sdk-[a-zA-Z0-9-]{16,}$/.test(v)
    ) {
      return {
        ok: false,
        name: 'noLiteralLDKey',
        detail: `Found a hardcoded LaunchDarkly key in initialize(). Use process.env.LD_CLIENT_SIDE_ID instead.`,
      };
    }
  }
  return { ok: true, name: 'noLiteralLDKey' };
}

export function initUsesEnvVar(src) {
  // Only run this rule on files that actually call `initialize(...)`.
  if (!/\binitialize\s*\(/.test(src)) return { ok: true, name: 'initUsesEnvVar' };
  const usesEnv =
    /process\.env\.LD_(CLIENT_SIDE_ID|SDK_KEY)/.test(src) ||
    /import\.meta\.env\.(VITE_|NEXT_PUBLIC_)?LD_CLIENT_SIDE_ID/.test(src);
  if (!usesEnv) {
    return {
      ok: false,
      name: 'initUsesEnvVar',
      detail: 'initialize() should read the key from process.env (or import.meta.env for Vite/Next), not from a literal or a variable assigned elsewhere.',
    };
  }
  return { ok: true, name: 'initUsesEnvVar' };
}

export function variationFallbacksTypeCheck(src) {
  // Catch obvious type-incoherent fallbacks: variation(..., null), boolVariation(..., 0) etc.
  const issues = [];
  const re = /\b(variation|boolVariation|stringVariation|numberVariation|jsonVariation)\s*\(\s*['"][^'"]+['"]\s*(?:,\s*[^,)]+\s*)?,\s*([^,)]+)\)/g;
  for (const m of src.matchAll(re)) {
    const method = m[1];
    const fallback = m[2].trim();
    if (fallback === 'null' || fallback === 'undefined') {
      issues.push(`${method}() uses ${fallback} as fallback`);
    }
    if (method === 'boolVariation' && !/^(true|false)$/.test(fallback)) {
      issues.push(`boolVariation() fallback should be true|false, got: ${fallback}`);
    }
    if (method === 'stringVariation' && !/^['"`]/.test(fallback)) {
      issues.push(`stringVariation() fallback should be a string literal, got: ${fallback}`);
    }
  }
  if (issues.length) {
    return { ok: false, name: 'variationFallbacksTypeCheck', detail: issues.join('; ') };
  }
  return { ok: true, name: 'variationFallbacksTypeCheck' };
}

export function contextHasKindAndKey(src) {
  // Look for context object literals passed to initialize / identify /
  // asyncWithLDProvider's `context:`. Heuristic — scan for objects that
  // look like LD contexts and check kind+key.
  const objLiteralRe = /\{[^{}]*?\bkey\s*:[^{}]*?\}/gs;
  const matches = [...src.matchAll(objLiteralRe)];
  if (!matches.length) {
    // No object containing `key` — irrelevant file, skip.
    return { ok: true, name: 'contextHasKindAndKey' };
  }
  // For each candidate object that contains `key:`, check it also contains kind:
  const offenders = matches.filter((m) => !/\bkind\s*:/.test(m[0]));
  if (offenders.length) {
    return {
      ok: false,
      name: 'contextHasKindAndKey',
      detail: `Found ${offenders.length} object literal(s) with \`key\` but no \`kind\`. LDContexts require both. First offender: ${offenders[0][0].slice(0, 120)}…`,
    };
  }
  return { ok: true, name: 'contextHasKindAndKey' };
}

export function noLDUserType(src) {
  if (/\bLDUser\b/.test(src) && !/\bLDContext\b/.test(src)) {
    return {
      ok: false,
      name: 'noLDUserType',
      detail: 'Found LDUser type but no LDContext. LDUser is the deprecated pre-context API.',
    };
  }
  return { ok: true, name: 'noLDUserType' };
}

export function noWrongLDMethods(src) {
  const wrong = [
    /\bclient\.getBoolean\s*\(/,
    /\bclient\.evaluate\s*\(/,
    /\bclient\.getConfig\s*\(/,        // Statsig name in an LD context
    /\buseLDFlag\s*\(/,                 // hallucinated React hook
  ];
  for (const re of wrong) {
    if (re.test(src)) {
      return {
        ok: false,
        name: 'noWrongLDMethods',
        detail: `Found a method name that does not exist in the LaunchDarkly SDK: ${re.source}`,
      };
    }
  }
  return { ok: true, name: 'noWrongLDMethods' };
}

// Assertions runnable on a package.json
export function packageJsonUsesCurrentLDVersions(pkgJson, resolvedVersions) {
  const deps = { ...(pkgJson.dependencies || {}), ...(pkgJson.devDependencies || {}) };
  const findings = [];

  for (const legacy of LEGACY_LD_NAMES) {
    if (deps[legacy]) {
      findings.push(`package.json depends on legacy "${legacy}" — use @launchdarkly/* instead`);
    }
  }

  for (const pkg of LD_PACKAGES) {
    if (deps[pkg] && resolvedVersions?.[pkg]) {
      // Strip caret/tilde for major-version comparison.
      const declared = deps[pkg].replace(/^[\^~]/, '');
      const declaredMajor = declared.split('.')[0];
      const currentMajor = resolvedVersions[pkg].split('.')[0];
      if (declaredMajor !== currentMajor) {
        findings.push(
          `${pkg} is pinned at major ${declaredMajor} (declared ${deps[pkg]}); current published major is ${currentMajor} (${resolvedVersions[pkg]})`,
        );
      }
    }
  }

  if (findings.length) {
    return { ok: false, name: 'packageJsonUsesCurrentLDVersions', detail: findings.join('; ') };
  }
  return { ok: true, name: 'packageJsonUsesCurrentLDVersions' };
}

export const SOURCE_ASSERTIONS = [
  noLegacyLDPackageNames,
  noStatsigImportsUnlessExperimentsPresent,
  noLiteralStatsigKey,
  noLiteralLDKey,
  initUsesEnvVar,
  variationFallbacksTypeCheck,
  contextHasKindAndKey,
  noLDUserType,
  noWrongLDMethods,
];
