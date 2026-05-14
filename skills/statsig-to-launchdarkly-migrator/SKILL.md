---
name: statsig-to-launchdarkly-migrator
description: Migrate JavaScript/TypeScript/React/Node.js code from the Statsig SDK to LaunchDarkly. Use whenever the user mentions migrating off Statsig, replacing Statsig with LaunchDarkly, porting feature gates or dynamic configs to LaunchDarkly, converting StatsigUser to LDContext, or moving Statsig session replay / autocapture to LaunchDarkly Observability. Also use when a user pastes Statsig SDK code and asks for the LaunchDarkly equivalent. Handles feature gates and dynamic configs only — experiments are flagged and left in place. CRITICAL: agentic tools routinely import a stale LaunchDarkly Node SDK pulled from training data; this skill always resolves the current version via live npm lookup before writing any code.
---

# Statsig → LaunchDarkly SDK Migrator

Convert Statsig SDK code (JavaScript, TypeScript, React, Node.js) to LaunchDarkly while preserving experiments, ensuring the latest SDK versions, and writing the LD Client-Side ID to `.env` via `ldcli`. Migrates feature gates and dynamic configs; preserves and flags experiments.

## When this skill runs

Trigger phrases include: "migrate from Statsig", "replace Statsig with LaunchDarkly", "port these flags to LaunchDarkly", "convert StatsigUser to LDContext", or pasted Statsig code with a request to translate it.

## Workflow

Run these phases in order. Do not skip phases.

### Phase 0 — Inventory

1. Scan the codebase for: `statsig-js`, `@statsig/react-bindings`, `@statsig/web-analytics`, `@statsig/session-replay`, `statsig-node`.
2. List every `checkGate`, `getConfig`, `useGateValue`, `useConfig`, `getExperiment`, `useExperiment`, `useLayer` call.
3. List every `Statsig.initialize` / `StatsigProvider` / `StatsigClient` construction site and the user/context object passed.
4. **Experiment gate**: if `getExperiment` / `useExperiment` / `useLayer` is found, mark those flags BLOCKED. The Statsig SDK stays. See [references/experiments.md](references/experiments.md).

Output a short inventory table to the user before proceeding.

### Phase 1 — Resolve latest SDK versions (REQUIRED)

**Do NOT write any `package.json` edits or `import` statements until this phase completes.** Agentic tools — including Claude — routinely emit stale LaunchDarkly Node SDK versions pulled from training data (`launchdarkly-node-server-sdk@5.x` is the most common stale name; the current package is `@launchdarkly/node-server-sdk`). Always resolve via live lookup.

Run:

```bash
node skills/statsig-to-launchdarkly-migrator/scripts/resolve-versions.mjs
```

The script writes `migration-versions.json` with the current published version of each LaunchDarkly package the migration touches. Use those versions in `package.json` and cite them in the migration summary. If the script fails (offline), fall back to [references/version-floors.md](references/version-floors.md) and surface a warning to the user.

### Phase 2 — Pull the LaunchDarkly Client-Side ID via `ldcli`

The migrated code MUST read the Client-Side ID from `.env`, never a string literal. Follow [references/sdk-key-setup.md](references/sdk-key-setup.md) — it handles:

- `ldcli` install detection + install (brew or curl)
- `ldcli login` (interactive — the token does not pass through Claude)
- Project / environment selection
- Writing `LD_CLIENT_SIDE_ID=...` (or `LD_SDK_KEY=...` for server SDKs) to `.env` / `.env.local`
- Ensuring `.env` is in `.gitignore`
- Never echoing the key back to the conversation

**Never reuse the Statsig SDK key.** Statsig keys (`client-...`) and LaunchDarkly Client-Side IDs are not interchangeable; the migrated code will silently fail against LD if you wire the wrong one. If a Statsig key string survives in the migrated output, the static evals will fail.

### Phase 3 — Translate code

Translate in this order: imports → initialization → context → flag evaluations → observability. Canonical patterns:

#### Imports

```javascript
// Statsig → LaunchDarkly
const statsig = require('statsig-js');                 // → const LDClient = require('launchdarkly-js-client-sdk');
import statsig from 'statsig-js';                      // → import { initialize } from 'launchdarkly-js-client-sdk';
import { StatsigProvider, useGateValue } from '@statsig/react-bindings';
                                                       // → import { asyncWithLDProvider, useFlags, useLDClient } from 'launchdarkly-react-client-sdk';
import { StatsigClient } from 'statsig-node';          // → import { init } from '@launchdarkly/node-server-sdk';
```

**Node.js call-out:** the current package is `@launchdarkly/node-server-sdk` (scoped). The legacy unscoped name `launchdarkly-node-server-sdk` is the artifact of stale training data — if it appears in any migrated file, the static evals fail.

#### Initialization (read key from env, wait for init, latest version)

```javascript
import { initialize } from 'launchdarkly-js-client-sdk';

const client = initialize(
  process.env.LD_CLIENT_SIDE_ID,   // never a literal
  context,
  { /* plugins only if needed — see Phase 3 observability */ }
);

try {
  await client.waitForInitialization(5);
} catch (err) {
  // variation() returns fallbacks until init completes; log and proceed
}
```

For Node.js use `init(process.env.LD_SDK_KEY)` from `@launchdarkly/node-server-sdk`.

#### Flag evaluation

| Statsig | LaunchDarkly | Notes |
| --- | --- | --- |
| `statsig.checkGate("gate_name")` | `client.variation("gate_name", false)` | Fallback MUST be `false`. |
| `statsig.getConfig("cfg").get("title", "Default")` | `client.jsonVariation("cfg", { title: "Default" }).title` | Provide a COMPLETE fallback object. |
| `useGateValue("gate_name")` | `useFlags().gateName` | React auto-camelCases. |
| `useConfig("homepage_config")` | `useFlags().homepageConfig` | React auto-camelCases. |

For typed SDKs (Node, server-side), prefer `boolVariation` / `stringVariation` / `numberVariation` / `jsonVariation` over the generic `variation`. Never use `null` or `undefined` as a fallback — see [references/flag-evaluation.md](references/flag-evaluation.md).

#### Context (Statsig user → LDContext)

```javascript
// Statsig
{ userID: "u-1", email: "a@b.com", custom: { tier: "pro" }, privateAttributes: { ssn: "xxx" } }

// LaunchDarkly
{
  kind: "user",
  key: "u-1",                  // REQUIRED — from userID
  email: "a@b.com",
  tier: "pro",                 // custom flattened to top level
  ssn: "xxx",
  _meta: { privateAttributes: ["ssn"] }
}
```

Full mapping (multi-context, customIDs, IP/userAgent inference) in [references/context-migration.md](references/context-migration.md).

#### Observability (only if Statsig session replay or autocapture is imported)

If `@statsig/web-analytics` or `@statsig/session-replay` appears, port to LaunchDarkly's Observability and SessionReplay plugins. If neither is imported, **do not add any plugins** — keep the init call lean. Full parameter mapping in [references/observability.md](references/observability.md).

### Phase 4 — Flag naming by SDK

LaunchDarkly SDKs handle flag names differently:

| SDK | Auto camelCase? | Example |
| --- | --- | --- |
| React | **Yes** (default) | `admin_panel_access` → `flags.adminPanelAccess` |
| JavaScript / Node / Python / Go / Java / iOS / Android | No | `admin_panel_access` stays `admin_panel_access` |

React-only: can disable with `reactOptions: { useCamelCaseFlagKeys: false }`. The migration summary must record both names for every flag so reviewers can cross-check.

### Phase 5 — Half-implement, then test

Do not flip to LaunchDarkly in prod. The release shape is:

1. **In LaunchDarkly UI/API**: create each migrated flag in the **test** environment. Set it OFF for everyone.
2. **In code**: ship the migrated SDK init + variation calls. Statsig keys are gone or fenced behind experiment-only paths.
3. **In test env**: deploy. Hit a route that exercises a migrated flag. Confirm via LD's Live Events that the expected context arrived and the expected variation was served (`ldcli flags get <key> --env test` cross-checks).
4. **In test env**: flip the flag on, verify the new code path actually executes.
5. **Only then** repeat steps 1, 3, 4 in prod environment.

### Phase 6 — Verification (the user runs this)

Before declaring success, surface this checklist to the user verbatim:

- [ ] `.env` contains `LD_CLIENT_SIDE_ID` (or `LD_SDK_KEY` for Node) and `.env` is gitignored
- [ ] `npm ls launchdarkly-js-client-sdk` (and any other LD packages) shows the versions resolved in Phase 1
- [ ] App boots without `waitForInitialization` timing out
- [ ] For one migrated flag, the LaunchDarkly Live Events page shows the expected context kind + key
- [ ] For one migrated flag, the served variation matches what's configured in the test env
- [ ] If experiments were detected, the Statsig SDK still initializes alongside LaunchDarkly

### Phase 7 — Generate the migration report

Write `migration-summary.json` to the project root. See [references/report-format.md](references/report-format.md) for the schema. Required fields: resolved SDK versions, list of migrated flags with both raw and camelCased names, list of blocked-by-experiment flags, list of failed items with reasons, observability migration delta (Statsig params lost in translation), and the verification checklist.

## What does NOT get migrated

- **Experiments** (`getExperiment`, `useExperiment`, `useLayer`) — preserved as-is, both SDKs run in parallel
- Feature gates that are *part of* an experiment — blocked, surfaced in the report
- Complex targeting rules — must be recreated in the LD dashboard manually
- Statsig-specific session replay parameters (`maxSessionDurationMs`, `recordConsoleErrors`) — no LaunchDarkly equivalent

## Evals

Two eval harnesses live under `evals/`:

- `evals/static/` — runs the skill against fixtures and asserts on the output (versions, imports, fallbacks, context shape, no leaked Statsig keys). CI-runnable: `node evals/static/run.mjs`.
- `evals/judge/` — LLM-as-judge scoring against a rubric. Slower and costlier; run manually before releasing skill changes: `node evals/judge/run.mjs`.

See `evals/README.md` for details and rubric.

## Output format the skill produces

1. Inventory table (Phase 0)
2. Resolved SDK versions table (Phase 1)
3. SDK key setup confirmation — never the key value itself (Phase 2)
4. Per-file migrated code blocks (Phase 3)
5. Flag-naming summary by SDK (Phase 4)
6. Test environment rollout checklist (Phase 5)
7. Verification checklist for the user (Phase 6)
8. `migration-summary.json` written to project root (Phase 7)

## Hard rules

- Never reuse a Statsig SDK key
- Never write a literal Client-Side ID in code — always `process.env.*`
- Never use `null` or `undefined` as a flag fallback
- Never emit `launchdarkly-node-server-sdk` (legacy unscoped name) — the current package is `@launchdarkly/node-server-sdk`
- Never migrate experiments
- Never skip Phase 1 (version resolution) — stale SDK versions are the #1 reliability failure for agentic migrations

## Reference docs

- LaunchDarkly JS SDK: https://launchdarkly.com/docs/sdk/client-side/javascript
- LaunchDarkly React SDK: https://launchdarkly.com/docs/sdk/client-side/react/react-web
- LaunchDarkly Node SDK: https://launchdarkly.com/docs/sdk/server-side/node-js
- LaunchDarkly Contexts: https://launchdarkly.com/docs/home/flags/contexts
- LaunchDarkly Observability: https://launchdarkly.com/docs/home/observability
- `ldcli`: https://github.com/launchdarkly/ldcli
- Statsig JS SDK: https://docs.statsig.com/client/javascript-sdk/
