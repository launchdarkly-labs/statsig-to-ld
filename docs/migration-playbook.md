# Migration playbook: Statsig → LaunchDarkly

This document covers everything **outside** this repo's automated surfaces.

This repo ships two automated surfaces:

- **The `statsig-to-ld` CLI** — migrates flag and metric definitions, targeting rules, and (via the `warehouse` subcommand) warehouse-native experimentation setup.
- **The Claude Code skill at [`skills/statsig-to-launchdarkly-migrator/`](../skills/statsig-to-launchdarkly-migrator/SKILL.md)** — rewrites Statsig SDK call sites to LaunchDarkly SDK call sites in JS / TS / React / Node application code.

If you've already read the [README](../README.md), you know what each surface does. This playbook is about the parts neither surface automates: project-level decisions (context-kind mapping, cutover sequencing, rollback strategy), things the CLI explicitly leaves to manual work (segments, gate prerequisites, layers, experiments, per-env SDK key migration), and the language-runtime cases the skill doesn't cover.

## The fundamental scoping decision

`statsig-to-ld` moves **definitions** (flag shells, targeting rules, metric configurations) from Statsig into LaunchDarkly. After running it end-to-end:

- Your LaunchDarkly project has flags with the same names, rules, rollouts, and overrides as Statsig.
- Your LaunchDarkly metrics dashboard has your metrics.
- **Your application still calls Statsig.** The LD flags evaluate; nothing reads them.

The migration isn't done until your application code reads from LaunchDarkly. That's outside this tool's scope.

## What the CLI does NOT do

### 1. SDK call-site rewrites

Statsig SDK calls in your application still hit Statsig. The CLI does not modify your code.

```js
// Before (and still, until you change it):
if (statsig.checkGate('show_banner', user)) { ... }

// After (you need to write this):
const ldUser = { kind: 'user', key: user.id, email: user.email };
if (ldClient.boolVariation('show_banner', ldUser, false)) { ... }
```

> **Automate this step with the bundled skill.** For JavaScript / TypeScript / React / Node.js, the [`skills/statsig-to-launchdarkly-migrator/`](../skills/statsig-to-launchdarkly-migrator/SKILL.md) Claude Code skill rewrites call sites, resolves current LD SDK versions via live `npm view`, wires the LD Client-Side ID via `ldcli`, and emits a `migration-summary.json` you can feed into `statsig-to-ld flags import`. It handles imports, initialization, contexts, flag evaluations, and observability — and flags experiments as blocked rather than migrating them silently. Use the skill for the call-site rewrites; use the steps below for the project-level decisions the skill can't make for you.

**Recommended approach:**

1. **Add the LaunchDarkly SDK alongside the Statsig SDK** — don't remove Statsig yet.
2. **Decide a context-kind mapping**. Statsig calls take a `user` object with `userID` and properties; LaunchDarkly takes a richer `context` object with one or more context kinds. Many teams start by mapping every Statsig user to an LD `user` context, even if they target on `companyID`-style attributes in Statsig today.
3. **Rewrite call sites**. Use the [skill](../skills/statsig-to-launchdarkly-migrator/SKILL.md) (recommended for JS/TS/React/Node), by hand (acceptable if you have <100 sites), or via a custom codemod. The mappings are:
   - `statsig.checkGate(name, user)` → `ldClient.boolVariation(name, ctx, false)`
   - `statsig.getConfig(name, user).get(key, default)` → `ldClient.jsonVariation(name, ctx, default)` (then read the key)
   - `statsig.getExperiment(name, user)` → use an LD flag for the experiment variant
4. **Validate** by running both SDKs in parallel for a period — see [Validation strategy](#validation-strategy).
5. **Cut over reads** to LD once parity is confirmed.
6. **Remove the Statsig SDK** once you're confident.

There's no automated codemod in this tool. Open an issue if you'd find one useful.

### 2. Statsig segments

Statsig segments (named user cohorts) are referenced by gates via `passes_segment` / `fails_segment` conditions. The CLI doesn't recreate them in LaunchDarkly — by default it skips any flag whose targeting references a segment (fail-closed). With `--accept-data-loss=segments` the flag imports but the segment reference is dropped.

**Approach:**

1. Browse your Statsig segments in the Console and note which ones are referenced by gates you plan to import.
2. Recreate them in LaunchDarkly. Options:
   - **By hand in the LD UI** — fine for <10 segments.
   - **Via Terraform** — use the [`launchdarkly_segment`](https://registry.terraform.io/providers/launchdarkly/launchdarkly/latest/docs/resources/segment) resource.
   - **Via `ldcli`** — if you have segment definitions in JSON, `ldcli` can create them in bulk.
3. Once the segments exist in LD with matching keys, you still need to **hand-edit the affected flags** to add LD `segmentMatch` clauses. Today the CLI drops the segment ref entirely; future versions may emit `segmentMatch` automatically.

A planned `segments export` subcommand will dump Statsig segment definitions to JSON to make step 1 + 2 tractable in bulk.

### 3. Statsig gate prerequisites

`passes_gate` / `fails_gate` conditions (one gate referencing another) are dropped the same way segments are (fail-closed). LaunchDarkly **does** support flag prerequisites natively, but the CLI doesn't translate them today.

**Approach:**

1. After `flags import` + `targeting import`, find flags whose Statsig source had `passes_gate` / `fails_gate` references (the targeting report flags them under `lossy_targeting`).
2. For each, set up an LD **flag prerequisite** in the LD UI: Flag settings → Prerequisites → Add prerequisite.

### 4. Statsig layers

Statsig's **layers** are an experiment-grouping primitive that allocates mutually exclusive traffic across experiments sharing a parameter space. LaunchDarkly's experiment model is different and the CLI does not migrate layers.

If you use layers heavily, layer-by-layer migration is its own project. Map each layer's experiments to LD flags first, then design the LD experiment + mutual-exclusion strategy explicitly.

### 5. Statsig experiments and holdouts

The CLI handles flag definitions and targeting; it does **not** set up LD experiments. Running experiments in LaunchDarkly requires:

- An LD **metric** (which `metrics convert` can create) or metric group
- An LD **experiment** definition that references the metric and a flag
- Randomization unit + audience setup

Statsig **holdouts** (long-running A/B groups that exclude users from other experiments) have no direct LD equivalent. You can approximate them with an LD flag + segment, but the design is up to you.

### 6. Per-environment SDK key migration

The CLI creates LD environments (auto-create on by default), but it doesn't fetch the new envs' SDK keys or update your application's runtime configuration. You'll need to grab the keys from the LD UI and wire them into wherever your app reads SDK config (env vars, secret manager, deployment config).

## Recommended cutover sequence

A reasonable order for a real migration. Adjust to your team's tolerance for parallel evaluation cost and rollback complexity.

1. **`statsig-to-ld analyze`** — scope the migration. Numbers: how many flags? how many metrics? how many use lossy features?
2. **Set up the LD project** — create the project, configure SCIM/SSO if needed, decide on context kinds, generate the API token for the CLI.
3. **SDK call-site rewrites** — invoke the [`skills/statsig-to-launchdarkly-migrator/`](../skills/statsig-to-launchdarkly-migrator/SKILL.md) skill (for JS / TS / React / Node) or hand-port (for other languages). The skill emits `migration-summary.json` with the canonical flag-key list the rest of the CLI flow keys off. Add the LD SDK alongside Statsig; don't switch reads yet.
4. **`statsig-to-ld flags import`** — creates shells. Off in every env, no production impact.
5. **Recreate segments** (manual) — for any flags you plan to target.
6. **`statsig-to-ld targeting import --dry-run`** — preview the targeting application. Review the report for `skipped_lossy` entries.
7. **`statsig-to-ld targeting import`** — apply the targeting. Strict by default; opt in via `--accept-data-loss=...` if needed. Flags now have the same targeting as Statsig, but your app still reads Statsig.
8. **`statsig-to-ld warehouse`** — *only if you use Statsig warehouse-native experimentation.* Sets up the LaunchDarkly side: data export integration, experimentation integration, and LD metric data sources. **Does not migrate metric definitions** — it writes `source-mapping.json` for step 9 to consume. Skip this step entirely if you're on Statsig Cloud event-based metrics only.
9. **`statsig-to-ld metrics convert`** — most likely to need manual cleanup (DATA LOSS warnings, unsupported metric types). Doing this after flags + targeting are validated avoids reworking orphan metrics. If you ran step 8, pass `--source-mapping source-mapping.json` so warehouse-native metrics bind to the data sources step 8 created; event-based metrics are converted the same way regardless. Metrics in LD don't affect anything until you reference them, so this step is still order-independent — it's just easier to triage at this point.
10. **Validate** — see [Validation strategy](#validation-strategy).
11. **Cut over reads** — flip your application to read from LD instead of Statsig. Keep Statsig writes in case of rollback.
12. **Soak** — let LD serve production for an agreed period.
13. **Remove the Statsig SDK** — once you're confident the migration is stable.

## Validation strategy

The high-stakes step is cutting over reads. The strategy that's easiest to get right is **shadow evaluation**:

- For each gate/config call your application makes, evaluate it against **both** Statsig and LaunchDarkly.
- Log when they disagree, with enough context to debug (flag key, user key, both results).
- Run shadow evaluation for some agreed period (days to weeks).
- Use Statsig as the authoritative result; flip to LD only when divergence rate is acceptably low.

A simple wrapper in your application:

```js
function checkGate(name, user) {
  const statsigResult = statsig.checkGate(name, user);
  if (FEATURE_LD_SHADOW_EVAL) {
    const ldResult = ldClient.boolVariation(name, toLDContext(user), false);
    if (statsigResult !== ldResult) {
      logger.warn('flag_divergence', { name, user: user.id, statsig: statsigResult, ld: ldResult });
    }
  }
  return statsigResult;
}
```

Acceptable divergence rate is your call. Some teams accept 0%; others accept up to 1% for the long tail of weird contexts (deleted users, bot traffic, etc.).

## Rollback strategy

The good news: Statsig is read-only from LaunchDarkly's perspective. The CLI only writes to LD. Until you actually cut application reads over to LD, **rolling back is free** — just don't deploy the SDK switch, or revert the deploy that did.

After cutover:

- **Keep Statsig writes flowing** for as long as you might want to roll back. Don't immediately remove the Statsig SDK or stop syncing changes.
- **Have a kill switch**. The cleanest version is a single LaunchDarkly flag that controls which SDK your application reads from. If you spot a regression, flip that flag back to Statsig and triage.
- **Don't delete LD flags or environments** until you're past your rollback window.

## API token permissions

For the CLI, the LaunchDarkly token needs to:

- **Read** flags, environments, metrics in the target project (for analyze, idempotency dedupe, env reconciliation)
- **Write** flags, metrics (for `flags import`, `metrics convert`, `targeting import`)
- **Create** environments (only if you let `targeting import` auto-create missing envs; turn off with `--no-create-envs`)

The built-in **Writer** role covers all of this. If your team uses custom roles, the minimum set is:

```
viewFlag, createFlag, updateFlag (for the target project)
viewMetric, createMetric (for the target project)
viewEnvironment, createEnvironment (if auto-creating; otherwise just view)
```

For shared automation (CI, scheduled re-imports), use a **service token** rather than a personal access token so the migration doesn't break when individuals leave.

## When the CLI is not the right tool

A few cases where you should not use this CLI:

- **You want to keep Statsig as your source of truth and write back to it from LD** — this is a one-way migration tool. Two-way sync is a different problem.
- **Your Statsig usage is dominated by experiments/holdouts/layers** — the CLI handles flag definitions well, but you'll spend most of your migration time on experiment-tier setup that the CLI doesn't address.
- **You have <10 flags and no metrics** — the migration is small enough that hand-creating in LD's UI is probably faster than wrangling API keys and running a CLI.

## Getting help

- Open an issue: [github.com/launchdarkly-labs/statsig-to-ld/issues](https://github.com/launchdarkly-labs/statsig-to-ld/issues)
- LaunchDarkly support: see your LD account's support channel
- Statsig support: see your Statsig account's support channel

The CLI is community-maintained best-effort under the LaunchDarkly Labs umbrella. It is not officially supported by LaunchDarkly.
