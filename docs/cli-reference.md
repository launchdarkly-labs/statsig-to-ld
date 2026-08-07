# `statsig-to-ld` CLI reference

Full manual flow + per-subcommand reference + warehouse-native walkthrough + AI-agent integration table.

If you're driving the CLI through an AI coding agent, the higher-level bootstrap is in the repo [README](../README.md) (`Agent Instructions` section). This document is for direct CLI users and for anyone who needs the long-form per-subcommand detail.

## Manual quick start

For running the CLI directly without an agent. (For agent-driven, see [Quick start (Claude Code)](../README.md#quick-start-claude-code) and the [Agent Instructions](../README.md#agent-instructions) in the README.)

```bash
# 1. Set API keys (recommended — avoids shell history exposure)
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY

# 2. Scope the migration: how much work, what won't import faithfully
statsig-to-ld analyze --ld-project my-project

# 3. Create flag shells — off in all envs, no production impact
statsig-to-ld flags import --all --ld-project my-project

# 4. Preview targeting — fail-closed by default
statsig-to-ld targeting import --all --ld-project my-project --dry-run

# 5. Apply targeting (review the dry-run report first)
statsig-to-ld targeting import --all --ld-project my-project

# 6. Convert metrics last — most likely to need manual cleanup, so do this
#    after flags + targeting are validated
statsig-to-ld metrics convert --all --ld-project my-project

# 7. Read the migration playbook before changing your app
cat docs/migration-playbook.md
```

## API key security

API keys can be provided three ways (precedence order):

1. **Command-line flags** (`--statsig-key`, `--ld-key`) — visible in shell history and `ps` output. Use only in CI/CD where keys come from a secrets manager.
2. **Environment variables** (`STATSIG_CONSOLE_KEY`, `LD_API_KEY`) — not in `ps`, but `export KEY=value` lands in history. Use the `read -rs` form below.
3. **Interactive prompt** — **most secure** for interactive use. If a key isn't provided via flag or env, the tool prompts with echo disabled.

```bash
# Set without history exposure
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY
```

The LaunchDarkly project key (not a secret) can be set once via `LD_PROJECT` instead of passing `--ld-project` on every command: `export LD_PROJECT=my-project`.

## Subcommands

### analyze

Read-only sizing report. Surveys gates, dynamic configs, environments, and metrics and tells you what will import faithfully, what will be [lossy](#lossy-targeting-features), and what will be skipped.

```bash
# Statsig-only analysis — no LD account needed yet
statsig-to-ld analyze --statsig-key console-...

# Full analysis including env-mapping preview
statsig-to-ld analyze --ld-project my-project

# Save the structured report alongside the table
statsig-to-ld analyze --ld-project my-project --output analyze.json
```

See `statsig-to-ld analyze --help` for the full flag list.

### flags import

Creates LaunchDarkly **flag shells** from Statsig feature gates and dynamic configs. Variations, default values, tags, and the maintainer are set; per-environment targeting is **not** — run [`targeting import`](#targeting-import) afterwards.

```bash
# Dry-run first
statsig-to-ld flags import --all --ld-project my-project --dry-run

# Import everything (gates + dynamic configs)
statsig-to-ld flags import --all --ld-project my-project

# Gates only, filtered by a Statsig tag
statsig-to-ld flags import --all --import-type gates --include-tag p0 \
  --ld-project my-project
```

**Idempotency**: dedupe is by sanitized LD key, not display name. Renaming a Statsig gate between runs does **not** create a duplicate.

See `statsig-to-ld flags import --help` for the full flag list.

### targeting import

Applies per-environment targeting (rules, rollouts, user/context targets) to flag shells previously created by `flags import`. Reconciles Statsig environments to LaunchDarkly environments by case-insensitive name; auto-creates missing LD envs (turn off with `--no-create-envs`).

```bash
# Strict (default): skip flags whose targeting would be lossy
statsig-to-ld targeting import --all --ld-project my-project

# Permissive: accept all lossy features (matches the predecessor lambda)
statsig-to-ld targeting import --all --accept-data-loss=all \
  --ld-project my-project

# Surgical: accept specific lossy features
statsig-to-ld targeting import --all \
  --accept-data-loss=segments,unreachable_rules \
  --ld-project my-project
```

> **Re-run caveat (v1.0)**: `targeting import` overwrites the per-env settings on every matching flag. If you hand-tune flag targeting in the LaunchDarkly UI after import, re-running this command will overwrite those edits. A future release will add a `--diff` preview default for re-runs ([roadmap](#planned-follow-ups)).

See `statsig-to-ld targeting import --help` for the full flag list.

### metrics convert

Converts Statsig metric definitions into LaunchDarkly metrics. Supports both Statsig Cloud and Warehouse Native metrics, with idempotent re-runs, parallel processing, and structured migration reports.

```bash
# List available metric names/types, then exit (only the Statsig key needed)
statsig-to-ld metrics convert --list

# Export every metric's raw Statsig JSON to a file, then continue (Statsig key
# only). Useful for debugging conversion — see "Debugging a conversion" below.
statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json

# Dry-run preview
statsig-to-ld metrics convert --all --dry-run

# Bulk convert
statsig-to-ld metrics convert --all --ld-project my-project

# Single metric
statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

# Include lossy conversions too (skipped by default — see
# "Statsig features not carried over" below)
statsig-to-ld metrics convert --all --ld-project my-project --convert-lossy

# Warehouse Native with a single data source
statsig-to-ld metrics convert --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Warehouse Native with per-source mapping
statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping sources.json
```

Where `sources.json` is:

```json
{"purchases_table": "snowflake-purchases-ds", "sessions_table": "snowflake-sessions-ds"}
```

#### Flags worth knowing more about

`--help` keeps every flag description to one line so the list stays scannable. These are the ones with more to them:

| Flag | Detail |
|---|---|
| `--ld-data-source` | Binds warehouse-native and ratio metrics to a LaunchDarkly data source. Effectively required for them: a ratio metric is **rejected** without one (HTTP 400), and other warehouse-native metrics are created unbound, collecting no data. Metric filters and measurement windows also only convert when a data source is bound. Use `--source-mapping` instead when different Statsig sources map to different LD data sources. |
| `--source-mapping` | Takes precedence over `--ld-data-source` for any Statsig source name it lists. Unlisted sources fall back to `--ld-data-source`. |
| `--concurrency` | Defaults to 4, deliberately low to stay under LaunchDarkly's API rate limiter. Raise it if your project's limits allow; lower it if you start seeing 429s. |
| `--convert-lossy` | Off by default. A lossy metric is one where converting would drop or approximate a Statsig feature, so it is recorded as `skipped_lossy` in the report with the reason, rather than being silently converted into something subtly different. Pass this to convert them anyway and accept the imperfect result. See "Statsig features not carried over" below. |
| `--dump-raw` | Writes every Statsig metric's raw JSON verbatim, all fields, then continues the run. Needs only the Statsig key. The fastest way to see what Statsig actually returned for a metric that converted oddly. See "Debugging a conversion" below. |
| `--list` | Prints name, type, and id for every metric, then exits without converting. Needs only the Statsig key. |
| `--default-unit` | Applies to numeric metrics, which LaunchDarkly requires a unit for. Defaults to `units` when unset. Examples: `$`, `ms`, `count`. |
| `--unit-type-mapping` | Maps Statsig unit types to LD context kinds, e.g. `{"companyID": "company"}`. Without a mapping, a non-`userID` unit type is lowercased as a best guess and warned about. |
| `--verbose` | Replaces the compact per-metric ticker with a line per metric showing status, name, key, and any error. |

#### Custom unit types (company-level experiments)

If your Statsig project uses unit types beyond `userID` (e.g. `companyID`, `teamID`), map them to your LD context kinds:

```bash
statsig-to-ld metrics convert --all --ld-project my-project \
  --unit-type-mapping unit-types.json
```

```json
{"companyID": "company", "teamID": "team"}
```

Without this mapping, non-`userID` unit types are lowercased (e.g. `companyID` → `companyid`) and a warning is emitted.

#### Metric type conversion

| Statsig type | LD kind | Status |
|---|---|---|
| `event_count_custom` | custom | Supported |
| `sum` | custom (isNumeric) | Supported |
| `mean` | custom (isNumeric, average) | Supported |
| `event_user` | custom | Supported |
| `event_user_window` | custom | Supported |
| `ratio` | custom + denominator | Supported — requires a warehouse data source (`--ld-data-source` / `--source-mapping`) |
| `funnel` | — | Requires LD metric group |
| `composite` | — | Not supported in LD |
| `percentile` | — | Not supported as LD type |

See `statsig-to-ld metrics convert --help` for the full flag list.

#### Debugging a conversion (`--dump-raw`)

When a metric converts incorrectly — or, for warehouse-native metrics, isn't recognized — the fastest way to diagnose it is to capture the raw Statsig definition. `--dump-raw <file>` writes every metric's JSON exactly as the Statsig Console API returns it (all fields, including ones the converter doesn't yet model), then continues with whatever else the command was doing:

```bash
# Just export the raw JSON and stop (only the Statsig key is needed)
statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json

# Export the raw JSON and preview the conversion in one safe pass (no writes to LD)
statsig-to-ld metrics convert --all --dry-run --dump-raw statsig-metrics-raw.json
```

The tool sees a metric only through this response, so the dump is exactly what it works from. It can contain warehouse table and column names — **review and redact before sharing**, and note that the JSON *keys/structure* (Statsig's schema) are what matter for debugging, not the *values* (which you can replace with placeholders).

## Lossy targeting features

`targeting import` is **fail-closed by default**: flags whose Statsig source uses any of the features below are skipped (with a `skipped_lossy` entry in the report). To import them anyway, opt in via `--accept-data-loss`:

```bash
# Accept all lossy features
--accept-data-loss=all

# Accept only specific features
--accept-data-loss=segments,prerequisites
```

| Feature | Why it's lossy |
|---|---|
| `passes_segment` / `fails_segment` | Statsig segments aren't auto-recreated in LD; the condition is dropped. See [migration playbook §2](migration-playbook.md#2-statsig-segments). |
| `passes_gate` / `fails_gate` | Gate prerequisites aren't auto-recreated; the condition is dropped. Set up LD flag prerequisites manually. |
| Custom `unit_id` (non-`userID`) | Targeting is squashed to LD's `user` context kind in v1; per-unit targeting fidelity is lost. |
| Multi-variant DC overrides | Statsig's override API is binary pass/fail per user; multi-variant fidelity is lost. |
| Unreachable trailing rules | Rules after a "public" (match-everyone) rule are dropped — they're unreachable in Statsig's first-match-wins model too. |

There's also a softer category — **approximated operators** — that import with a warning but do not fail-closed:

| Operator | Approximation |
|---|---|
| `version_gte` | Emitted as `semVerGreaterThan` (LD has no `semVerGreaterThanOrEqual`) |
| `version_lte` | Emitted as `semVerLessThan` |

## Warehouse Native Migration

The `warehouse` subcommand sets up the LaunchDarkly side of a Statsig warehouse-native experimentation project — data export integration, experimentation integration, and LD metric data sources. **It does not migrate metric definitions.** After `warehouse` completes, run [`statsig-to-ld metrics convert`](#metrics-convert) to migrate the warehouse-native metric definitions, using the `source-mapping.json` that `warehouse` writes.

This boundary is deliberate: the warehouse subcommand handles the parts that are unique to warehouse-native (interactive wizard for warehouse credentials, SQL setup scripts, data source schema discovery via LD's preview API), and `metrics convert` handles the parts that are common across all Statsig metrics (DATA LOSS detection on event filters, unit-type mapping, idempotent re-runs, structured warnings).

### Already have LD data sources? Skip `warehouse`

The **only** reason to run `warehouse` is to *create* LaunchDarkly metric data sources. If they already exist — set up in the LD UI, managed via Terraform, or provisioned for you as part of your account — **don't run `warehouse` at all.** Go straight to `metrics convert` and tell it which data source each warehouse-native metric should bind to, in one of two ways:

**One data source for everything** — pass `--ld-data-source <ld-data-source-key>`; every warehouse-native metric binds to that single source:

```bash
statsig-to-ld metrics convert --all --ld-project my-project \
  --ld-data-source snowflake-prod
```

**Per-source mapping** — hand-write the same `source-mapping.json` that `warehouse` would have produced, then pass `--source-mapping`. It's a flat JSON object of **Statsig metric source name → LD data source key**:

```json
{
  "purchases_table": "snowflake-purchases-ds",
  "sessions_table": "snowflake-sessions-ds"
}
```

```bash
statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping source-mapping.json
```

Finding the two names:

- **Statsig metric source name** (the JSON keys) — each warehouse-native metric's `metricSourceName`, as returned by the Statsig metrics API / shown in the Statsig console. To enumerate every source at once, run `statsig-to-ld warehouse --dry-run`: it fetches the metric sources and writes each one's `name` to its export file without changing anything in LD.
- **LD data source key** (the JSON values) — the key of the existing metric data source in LaunchDarkly.

A warehouse-native metric whose source resolves through neither flag is still created, but **without** a data source binding (you'll see a `no LD data source specified` warning), and ratio metrics are rejected by LD at creation without one. Event-based (Statsig Cloud) metrics never need a data source, so a project with no warehouse-native metrics needs neither flag.

### How it works

Three phases:

1. **Export** — Fetches the warehouse connection config and metric_sources from Statsig (or loads them from a previously-saved JSON export file).
2. **Warehouse setup** — Sets up data export + experimentation integrations in LaunchDarkly via an interactive wizard (Snowflake, BigQuery, Databricks, Redshift). Auto-detects and skips integrations that already exist.
3. **Data sources** — Creates LD metric data sources, using LD's preview API to discover real warehouse column schemas. Writes `source-mapping.json` mapping each Statsig metric source name to the LD data source key it created.

After Phase 3 completes, the next step is `statsig-to-ld metrics convert --source-mapping source-mapping.json` to migrate metric definitions bound to those data sources. The `warehouse` subcommand prints this hand-off command at the end of every successful run.

### Quick start

```bash
# 1. Preview (no LD changes)
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY \
  --dry-run

# 2. Set up the warehouse side (integrations + data sources)
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production

# 3. Migrate metric definitions using the source-mapping.json from step 2
statsig-to-ld metrics convert --all \
  --ld-project my-project \
  --source-mapping source-mapping.json
```

### From an export file

Export from Statsig once, then run subsequent steps from the JSON file. Useful when iterating on Phase 2 / Phase 3 settings without re-hitting Statsig.

```bash
# Export to a statsig_export_*.json file (no LD changes)
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY --dry-run

# Set up integrations + data sources from the file (no Statsig key needed)
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production \
  --statsig-export-file statsig_export_2026-05-13_120000.json
```

### Resuming a failed run

If integration setup or data source creation fails partway through, use `--resume` to pick up where it left off. Progress is checkpointed to `migration_state.json`.

```bash
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production \
  --statsig-export-file export.json \
  --resume
```

### Running only one phase

```bash
# Phase 2 only — set up integrations, stop before creating data sources
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY --ld-project my-project --ld-environment production \
  --statsig-export-file export.json --only warehouse

# Phase 3 only — skip the integrations wizard (assumes integrations exist in LD),
# create data sources, write source-mapping.json
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY --ld-project my-project --ld-environment production \
  --statsig-export-file export.json --only data-sources
```

### Warehouse flags

| Flag | Default | Description |
|---|---|---|
| `--statsig-key` | — | Statsig Console API key (or `STATSIG_CONSOLE_KEY` env) |
| `--statsig-url` | Statsig Cloud | Statsig API base URL override |
| `--statsig-export-file` | — | Load Statsig data from a JSON export file |
| `--ld-key` | — | LaunchDarkly API access token (or `LD_API_KEY` env) |
| `--ld-url` | US Cloud | LaunchDarkly API base URL (for EU/FedRAMP) |
| `--ld-project` | — | LaunchDarkly project key (required) |
| `--ld-environment` | — | LaunchDarkly environment key (required) |
| `--dry-run` | `false` | Preview data source mapping without writing to LD (still writes `source-mapping.json` so you can review it) |
| `--resume` | `false` | Resume from `migration_state.json` |
| `--only` | — | Run only `warehouse` (Phase 2) or `data-sources` (Phase 3) |
| `--overwrite` | `false` | Overwrite existing entities in LD |
| `--verbose` | `false` | Show detailed API request/response info |
| `--no-color` | `false` | Disable colored terminal output |

### Supported warehouse types

| Warehouse | Data export | Experimentation | Interactive setup |
|---|---|---|---|
| Snowflake | Automated (SQL script + connection test) | Automated (SQL script + verification) | Yes |
| BigQuery | Manual (LD UI) | Automated (service account key) | Yes |
| Databricks | Manual (LD UI) | Automated (access token) | Yes |
| Redshift | Manual (LD UI) | Automated (IAM role + SQL scripts) | Yes |

> **Internal API endpoints.** The metric data source CRUD operations use LaunchDarkly's `/internal/` API endpoints. These accept API key authentication but are not part of the public API and may change without notice.

## Statsig features not carried over (metrics convert)

Some Statsig features can't be reproduced faithfully in LaunchDarkly. A metric whose conversion would **drop or approximate** one of these is treated as **lossy**, and by default it is **skipped** — recorded as `skipped_lossy` in the report — rather than silently converted into something subtly different. Pass `--convert-lossy` to convert them anyway and accept the imperfect result; the specific reasons then appear as warnings on each converted metric. (This mirrors `targeting import`'s `--accept-data-loss`.)

**Always lossy — skipped by default:**

| Feature | Effect if converted |
|---|---|
| Per-unit capping | No per-user-per-day value cap |
| Log transform | Values not log-transformed; distribution shape may differ |
| Daily participation rate | Falls back to standard binary conversion (different aggregation) |
| Count distinct (event-based metric) | LD counts all occurrences instead |
| Metadata aggregation | LD aggregates the `track()` metricValue instead |
| Multiple metric events | Only the first event is used; the rest are ignored |

**Conditionally lossy — converted faithfully in the common case; lossy (and skipped) only when noted:**

| Feature | Converts when… | Lossy (skipped) when… |
|---|---|---|
| Winsorization | numeric or count metric (mapped to LD `winsorLowerPercentile`/`winsorUpperPercentile`) | occurrence metric (non-numeric average), where LD can't apply it |
| Custom rollup window | a warehouse data source is bound (mapped to LD window offsets via `--ld-data-source`) | no data source is bound (LD windows require a snowflake source) |
| Metric filter criteria | a warehouse-native metric with a bound data source and every criterion mappable (see below) | a cloud metric, no data source bound, or any criterion unmappable |

### Metric filter criteria

Statsig filter criteria on a **warehouse-native** metric convert to a LaunchDarkly metric filter. Ratio metrics carry a filter per term, so the numerator and denominator convert independently.

Statsig combines multiple criteria on one term with AND, and multiple values within one criterion with OR. That maps onto a LaunchDarkly `and` group of clauses, which is the same model.

| Statsig condition | LaunchDarkly operator |
|---|---|
| `in`, `=` | `in` |
| `not_in` | `in` negated |
| `contains` | `contains` |
| `not_contains` | `contains` negated |
| `starts_with` / `ends_with` | `startsWith` / `endsWith` |
| `>` `>=` `<` `<=` | `greaterThan` / `greaterThanOrEqual` / `lessThan` / `lessThanOrEqual` |
| `non_null` | `exists` |
| `is_null` | `exists` negated |
| `is_true` / `is_false` | `in` with the boolean `true` / `false` |

**Not converted.** `sql_filter` is arbitrary SQL. `after_exposure` and `before_exposure` compare a column against each unit's exposure timestamp, which LaunchDarkly's date operators cannot express (they compare against a fixed date). A criterion is also unmappable if it sets `nullVacuousOverride`, has no column, uses a context-attribute type (`user`, `user_custom`), carries a non-numeric value for a numeric comparison, carries more than one value for a numeric comparison, or has an empty-string value.

`is_true` and `is_false` assume the column really holds a boolean. A warehouse filter compares the column's text form, and a boolean column renders as `true`/`false`, so the match lines up. A column that stores `1`/`0` or `"TRUE"` instead will not match, and the filter selects no rows.

**All or nothing per term.** If any criterion on a term is unmappable, no filter is emitted for that term and the metric stays lossy. Because criteria are AND-ed, applying only the mappable subset would *widen* what the metric matches, producing a metric that looks converted but silently counts more rows than the original. The warning lists every dropped criterion so it can be rebuilt by hand.

> **Requirements.** Filters must be enabled for the target LaunchDarkly project, and they currently compute only on **Snowflake**-backed data sources. A filter saved against another warehouse type persists but fails when results are computed. Filters also need a bound data source: without one LaunchDarkly treats the metric as SDK-hosted, where the same clause would mean a JSON payload lookup rather than a warehouse column, so those criteria are reported as lossy instead.

## EU / FedRAMP instances

```bash
statsig-to-ld <subcommand> ... --ld-url https://app.eu.launchdarkly.com
```

All subcommands accept `--ld-url` and `--statsig-url` overrides.

## Planned follow-ups

These are explicit non-goals for v1.0; they're tracked for follow-up releases:

- **`segments export`** — dump Statsig segment definitions to JSON so users can recreate them in LD by hand, via Terraform, or via `ldcli`.
- **`targeting import --update-existing --diff`** — preview-before-overwrite for re-running targeting on already-targeted flags.
- **Split.io and Unleash sources** — out of scope for v1; the codebase has hooks but no logic.

## Releasing a new version (contributors)

Releases are driven by Git tags. Pushing a `v*` tag triggers CI to cross-compile binaries for macOS, Linux, and Windows.

> **The recommended path is to build from source with `go build`** (see the [README](../README.md#cli-build-from-source)). The tagged release binaries are published mainly for Linux, and for shipping major collections of updates and bug fixes.

```bash
git tag v0.2.0
git push origin v0.2.0
```

Release notes are generated from commits since the previous tag.

## Using with AI coding agents

Two AI surfaces live in this repo:

1. **CLI-driving surfaces** — for `analyze`, `flags import`, `targeting import`, `metrics convert`, `warehouse`. Backed by the agent-agnostic [`AGENTS.md`](../AGENTS.md) (build, API-key handling, recommended sequence, per-subcommand usage, report analysis, troubleshooting). Treat it as authoritative. The warehouse subcommand has additional shim-only detail in [`.claude/agents/statsig-warehouse-migrator.md`](../.claude/agents/statsig-warehouse-migrator.md).
2. **SDK-rewrite skill** — for the application-code rewrite step (Statsig SDK calls → LaunchDarkly SDK calls). Lives at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](../skills/statsig-to-launchdarkly-migrator/SKILL.md). Standalone Claude Code skill with progressive-disclosure references, helper scripts, and eval harnesses — **not** a shim over `AGENTS.md`.

The CLI-driving surfaces each `@`-import `AGENTS.md`, so a change there propagates to every agent listed below:

| Agent | File | How it loads |
|---|---|---|
| **Codex** + any agent reading the `AGENTS.md` convention | [`AGENTS.md`](../AGENTS.md) | OpenAI Codex auto-loads `AGENTS.md` from the repo root as part of its system prompt; other agents that follow the convention (recent Cursor, Sourcegraph, etc.) do the same. Point any other agent at it manually. |
| **Cursor** | [`.cursor/rules/statsig-to-ld.mdc`](../.cursor/rules/statsig-to-ld.mdc) | Auto-attaches when the conversation matches the rule's description (Statsig→LD migration topics). |
| **GitHub Copilot** | [`.github/copilot-instructions.md`](../.github/copilot-instructions.md) | Auto-loaded into every Copilot Chat session in this repo. |
| **Aider** | [`.aider.conf.yml`](../.aider.conf.yml) | Project config `read:` list auto-loads `AGENTS.md` as read-only context for every Aider session in this repo. |
| **Claude Code** (skill) | [`.claude/skills/statsig-to-ld/SKILL.md`](../.claude/skills/statsig-to-ld/SKILL.md) | Auto-loads on trigger phrases (subcommand names, API-key env vars, report filenames). |
| **Claude Code** (subagent — end-to-end CLI) | [`.claude/agents/statsig-to-ld.md`](../.claude/agents/statsig-to-ld.md) | Invoke via the Task tool for a delegated end-to-end migration in a separate context. |
| **Claude Code** (subagent — warehouse only) | [`.claude/agents/statsig-warehouse-migrator.md`](../.claude/agents/statsig-warehouse-migrator.md) | Invoke via the Task tool when the user only needs Path E (warehouse-native experimentation). Encodes the wizard flow, SQL setup, and resume semantics that aren't in `AGENTS.md`. |
| **Claude Code** (SDK-rewrite skill) | [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](../skills/statsig-to-launchdarkly-migrator/SKILL.md) | Symlink/copy into `~/.claude/skills/` (see [Installation](../README.md#sdk-rewrite-skill)); auto-loads when the conversation mentions migrating Statsig SDK code to LaunchDarkly. **Different concern** from the CLI shims above — handles application-code rewrites, not the CLI. |

If your agent isn't listed, point it at [`AGENTS.md`](../AGENTS.md) (for the CLI) or [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](../skills/statsig-to-launchdarkly-migrator/SKILL.md) (for SDK rewrites) directly.
