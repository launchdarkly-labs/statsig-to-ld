# statsig-to-ld

A CLI for migrating from Statsig to LaunchDarkly.

> **Important — read this first.** This tool migrates **flag and metric definitions, and targeting rules**, from Statsig to LaunchDarkly. For the SDK code rewrite (Statsig SDK calls → LaunchDarkly SDK calls), use the bundled Claude Code skill at [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md). It does not run experiments. It does not recreate every Statsig feature 1:1. See [`docs/migration-playbook.md`](docs/migration-playbook.md) for what you still need to do yourself.

## Overview

Five CLI subcommands plus a bundled Claude Code skill:

| Surface | What it does | Writes to |
|---|---|---|
| [`analyze`](#analyze) (CLI) | Read-only Statsig project survey + sizing report | Nothing |
| [`flags import`](#flags-import) (CLI) | Create LaunchDarkly flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| [`targeting import`](#targeting-import) (CLI) | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| [`metrics convert`](#metrics-convert) (CLI) | Convert Statsig metric definitions | LaunchDarkly |
| [`warehouse`](#warehouse-native-migration) (CLI) | Migrate Statsig warehouse-native experimentation (integration + data sources + metrics) | LaunchDarkly + warehouse |
| [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md) (Claude Code skill) | Rewrite Statsig SDK calls → LaunchDarkly SDK calls in your codebase | Your source files |

## Prerequisites

- Go 1.24+ (to build from source) or a pre-built binary from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API access token** (`api-xxx`) — create at **Account settings → Authorization → Access tokens** with a role that can write flags, metrics, and (optionally) environments in the target project. The Writer role works.

## Installation

### From source

```bash
go build -o statsig-to-ld .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-to-ld/cmd.version=1.0.0" \
  -o statsig-to-ld .
```

### Pre-built binaries

Download from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page.

## Quick start — full migration

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
# Dry-run preview
statsig-to-ld metrics convert --all --dry-run

# Bulk convert
statsig-to-ld metrics convert --all --ld-project my-project

# Single metric
statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

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
| `ratio` | — | Not yet supported in LD |
| `funnel` | — | Requires LD metric group |
| `composite` | — | Not supported in LD |
| `percentile` | — | Not supported as LD type |

See `statsig-to-ld metrics convert --help` for the full flag list.

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
| `passes_segment` / `fails_segment` | Statsig segments aren't auto-recreated in LD; the condition is dropped. See [migration playbook §2](docs/migration-playbook.md#2-statsig-segments). |
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

The `warehouse` subcommand migrates Statsig's warehouse-native experimentation setup to LaunchDarkly. It handles the full pipeline: warehouse integration setup, metric data source creation, and metric migration.

### How it works

The migration runs in three phases:

1. **Export** — Fetches warehouse connection config, metric sources, and metrics from Statsig (or loads from a JSON export file)
2. **Warehouse setup** — Sets up data export and experimentation integrations in LaunchDarkly (interactive wizards for Snowflake, BigQuery, Databricks, Redshift)
3. **Migrate** — Creates metric data sources and metrics in LaunchDarkly, using the warehouse preview API to discover real column schemas

### Quick start

```bash
# 1. Preview what will happen (no LD changes)
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY \
  --dry-run

# 2. Run the full migration
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production
```

### From an export file

You can export Statsig data to a JSON file first, then run the migration from that file. This is useful for reviewing the data before migrating, or for running the migration in a different environment.

```bash
# Export first (dry-run saves a statsig_export_*.json file)
statsig-to-ld warehouse \
  --statsig-key console-YOUR_KEY --dry-run

# Migrate from the export file (no Statsig key needed)
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production \
  --statsig-export-file statsig_export_2026-05-13_120000.json
```

### Resuming a failed migration

If the migration fails partway through, use `--resume` to pick up where it left off. The tool saves progress to `migration_state.json` and skips already-created entities.

```bash
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY \
  --ld-project my-project \
  --ld-environment production \
  --statsig-export-file export.json \
  --resume
```

### Migrating only data sources or metrics

```bash
# Only create data sources (skip metrics)
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY --ld-project my-project --ld-environment production \
  --statsig-export-file export.json --only data-sources

# Only create metrics (skip data sources)
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY --ld-project my-project --ld-environment production \
  --statsig-export-file export.json --only metrics
```

### Skipping warehouse setup

If data export and experimentation integrations are already configured in LD, the tool detects them automatically and skips setup. To skip the check entirely:

```bash
statsig-to-ld warehouse \
  --ld-key api-YOUR_KEY --ld-project my-project --ld-environment production \
  --statsig-export-file export.json --skip-warehouse
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
| `--dry-run` | `false` | Export and preview mapping without writing to LD |
| `--resume` | `false` | Resume from `migration_state.json` |
| `--skip-warehouse` | `false` | Skip warehouse connection setup (Phase 2) |
| `--only` | — | Migrate only `data-sources` or `metrics` |
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

### Warehouse metric type mapping

| Statsig type | LD mapping | Notes |
|---|---|---|
| `sum` | numeric, unitAggregationType: sum | |
| `mean` | numeric, unitAggregationType: average | |
| `event_count` | numeric, unitAggregationType: sum | |
| `count_distinct` | numeric, unitAggregationType: sum | |
| `percentile` | numeric, analysisType: percentile | eventDefault disabled |
| `user` / `user_count` | non-numeric (conversion) | |
| `conversion` | non-numeric (conversion) | |
| `retention` | non-numeric (conversion) | |
| `ratio` | — | Skipped (no LD equivalent) |
| `funnel` | — | Skipped (no LD equivalent) |
| `composite` | — | Skipped (no LD equivalent) |
| `undefined` | — | Skipped (metric not configured) |

> **Internal API endpoints.** The metric data source CRUD operations use LaunchDarkly's `/internal/` API endpoints. These endpoints accept API key authentication but are not part of the public API and may change without notice.

## Statsig features not carried over (metrics convert)

| Feature | Warning | Impact |
|---|---|---|
| Event filter criteria | `DATA LOSS` | LD metric matches all events, not just the filtered subset. Manual filter setup required. |
| Winsorization | Outlier clipping not applied | Experiment results may be more sensitive to outliers |
| Per-unit capping | Daily cap not applied | No per-user-per-day value cap |
| Log transform | Values not log-transformed | Distribution shape may differ |
| Custom rollup windows | Measurement windows not applied | LD uses full experiment duration |
| Daily participation rate | Uses standard binary conversion | Different aggregation method |
| Count distinct | Counts all occurrences instead | Higher counts than Statsig |
| Metadata aggregation | Aggregates `track()` metricValue | Ensure events send correct value |

Metrics with these features are still converted; the warning appears per-entry in the report.

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

Releases are driven by Git tags. Pushing a `v*` tag triggers CI to cross-compile binaries for macOS, Linux, and Windows and publish them to the [Releases page](https://github.com/launchdarkly-labs/statsig-to-ld/releases).

```bash
git tag v0.2.0
git push origin v0.2.0
```

Release notes are generated from commits since the previous tag.

## Using with AI coding agents

This repo ships a single, agent-agnostic operator guide at [`AGENTS.md`](AGENTS.md) covering build, API-key handling, the recommended migration sequence, per-subcommand usage, report analysis, and troubleshooting. Treat it as authoritative.

Agent-specific shims point back at the same guide so each agent's native discovery surface works without duplicating content:

| Agent | File | How it loads |
|---|---|---|
| **Codex** + any agent reading the `AGENTS.md` convention | [`AGENTS.md`](AGENTS.md) | OpenAI Codex auto-loads `AGENTS.md` from the repo root as part of its system prompt; other agents that follow the convention (recent Cursor, Sourcegraph, etc.) do the same. Point any other agent at it manually. |
| **Cursor** | [`.cursor/rules/statsig-to-ld.mdc`](.cursor/rules/statsig-to-ld.mdc) | Auto-attaches when the conversation matches the rule's description (Statsig→LD migration topics). |
| **GitHub Copilot** | [`.github/copilot-instructions.md`](.github/copilot-instructions.md) | Auto-loaded into every Copilot Chat session in this repo. |
| **Aider** | [`.aider.conf.yml`](.aider.conf.yml) | Project config `read:` list auto-loads `AGENTS.md` as read-only context for every Aider session in this repo. |
| **Claude Code** (skill) | [`.claude/skills/statsig-to-ld/SKILL.md`](.claude/skills/statsig-to-ld/SKILL.md) | Auto-loads on trigger phrases (subcommand names, API-key env vars, report filenames). |
| **Claude Code** (subagent) | [`.claude/agents/statsig-to-ld.md`](.claude/agents/statsig-to-ld.md) | Invoke via the Task tool for a delegated end-to-end migration in a separate context. |

Each shim `@`-imports `AGENTS.md`, so a change there propagates to every agent. If your agent isn't listed, point it at `AGENTS.md` directly.

## See also

- [`docs/migration-playbook.md`](docs/migration-playbook.md) — what this tool **doesn't** do (SDK rewrites, layers, experiments, holdouts, segment recreation, cutover, rollback)
- [`AGENTS.md`](AGENTS.md) — operator guide for AI agents driving the CLI
- [`CHANGELOG.md`](CHANGELOG.md)
- [`CONTRIBUTING.md`](CONTRIBUTING.md)
- [`SECURITY.md`](SECURITY.md)
