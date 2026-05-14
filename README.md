# statsig-to-ld

End-to-end migration from Statsig to LaunchDarkly: SDK code rewrites + flag, targeting, and metric definitions.

> **Important — read this first.** This repo ships **two surfaces**: a Go CLI (`statsig-to-ld`) that migrates flag, targeting, and metric *definitions* via the Statsig and LaunchDarkly REST APIs, and a Claude Code skill (`skills/statsig-to-launchdarkly-migrator/`) that rewrites Statsig SDK calls in your application code. Neither recreates experiments. See [`docs/migration-playbook.md`](docs/migration-playbook.md) for what neither tool does.

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

## Quick start (Claude Code)

Paste this into Claude Code (or any Claude interface with this repo on the filesystem):

```
Read the README at https://github.com/launchdarkly-labs/statsig-to-ld and follow the Agent Instructions section to help me migrate from Statsig to LaunchDarkly.
```

Claude will then run the [Agent Instructions](#agent-instructions) below — asking which paths you need, prompting you to export credentials in your shell, and running the appropriate surface for each path.

For users running the CLI directly without an agent, see [Manual quick start](#manual-quick-start).

## Agent Instructions

This section is the **path-selection bootstrap**: ask the user what to migrate, then delegate to the canonical guide for each path. The detail lives elsewhere — don't restate it here:

- **Path A** (SDK code rewrite): canonical guide is [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md). Load it and run its phases.
- **Paths B / D / C** (CLI): canonical guide is [`AGENTS.md`](AGENTS.md). Use its credentials, sequence, and per-subcommand sections.

### Step 1 — Optional pre-scope

If the user hasn't yet, propose `statsig-to-ld analyze` as a read-only sizing pass (counts of gates/configs/envs/metrics, flagged lossy targeting features, unsupported metric types). Needs only `STATSIG_CONSOLE_KEY`. See [`AGENTS.md` § Recommended migration sequence](AGENTS.md) for the analyze surface; skip if the user wants to dive in.

### Step 2 — Ask which paths

Ask which the user wants (multi-select):

- **A — Migrate SDK code** → the skill at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md)
- **B — Create LD flag shells** → `statsig-to-ld flags import` (see AGENTS.md)
- **D — Migrate targeting rules** → `statsig-to-ld targeting import` (see AGENTS.md)
- **C — Migrate metrics** → `statsig-to-ld metrics convert` (see AGENTS.md)

If multiple are selected, execute in order **A → B → D → C**:

1. **A first** — the skill emits `migration-summary.json` with the canonical flag-key list that B should match.
2. **B before D** — targeting rules apply to flag shells, which must already exist.
3. **C last** — metrics are independent of the flag work and most likely to need manual cleanup (DATA LOSS warnings, unsupported types); cheaper to triage after the flag side is settled.

### Step 3 — Credentials (split by surface)

**Never ask the user to paste keys into the chat.** The two surfaces have different key flows:

- **Path A — skill.** Phase 2 of the skill calls `ldcli` interactively and writes `LD_CLIENT_SIDE_ID` (or `LD_SDK_KEY`) to `.env` without the key passing through the conversation. See [`skills/statsig-to-launchdarkly-migrator/references/sdk-key-setup.md`](skills/statsig-to-launchdarkly-migrator/references/sdk-key-setup.md).
- **Paths B / D / C — CLI.** Instruct the user to export the two keys in the same shell they'll run the CLI from (no chat exposure, no history exposure):

  ```bash
  read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY   # console-... from Statsig
  read -rs LD_API_KEY && export LD_API_KEY                     # api-... from LaunchDarkly
  ```

  Full handling rules and precedence in [`AGENTS.md` § API key handling](AGENTS.md). Wait for the user to confirm before running any CLI command.

### Step 4 — Run each path

- **Path A.** Load [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) and run its seven phases as written. The skill handles its own credentials, version resolution, code translation, and report.
- **Paths B / D / C.** Follow [`AGENTS.md`](AGENTS.md) for the per-subcommand flow (`--dry-run` first, fail-closed semantics for targeting, `--accept-data-loss` opt-ins, idempotency notes, report locations). Confirm the LD project key with the user before applying. Always dry-run before apply.

### Step 5 — Summarize results

After all selected paths complete, present a per-path summary:

- **Path A**: files changed, flag keys in `migration-summary.json`, items blocked by experiments
- **Path B**: flags created, flags skipped (already existed), incompatible flags
- **Path D**: targeting applied, flags skipped as `skipped_lossy`, approximated operators
- **Path C**: metrics converted, metrics skipped, DATA LOSS warnings
- **Next steps**: experiment migration (manual in LD), parallel SDK testing checklist, segments to recreate in LD UI, [`docs/migration-playbook.md`](docs/migration-playbook.md) for the rest

## Credentials reference

| Path | Required | How to provide |
|---|---|---|
| Path A — SDK code | `LD_CLIENT_SIDE_ID` (client SDKs) or `LD_SDK_KEY` (Node SDK) | Inserted into `.env` interactively by the skill via `ldcli`; never via chat |
| Paths B / D / C — CLI | `LD_API_KEY` + `STATSIG_CONSOLE_KEY` | Shell export (`read -rs`), or `--ld-key` / `--statsig-key` flags, or interactive prompt |

Where to find each key:

- **`LD_API_KEY`** — LaunchDarkly → Account settings → Authorization → Access tokens. Needs the **Writer** role (or finer-grained perms covering flags, metrics, environments) in the target project.
- **`LD_CLIENT_SIDE_ID` / `LD_SDK_KEY`** — LaunchDarkly → Account settings → Projects → [project] → [environment]. Use Client-Side ID for browser/React; Server SDK key for Node.
- **`STATSIG_CONSOLE_KEY`** — Statsig Console → Project Settings → Keys & Environments. The `console-...` Console API key, not the SDK keys.

Paths B / D / C share `LD_API_KEY` + `STATSIG_CONSOLE_KEY` — export once and all three CLI paths work.

## Prerequisites

- Go 1.24+ (to build from source) or a pre-built binary from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API access token** (`api-xxx`) — create at **Account settings → Authorization → Access tokens** with a role that can write flags, metrics, and (optionally) environments in the target project. The Writer role works.
- For Path A only: Claude Code (or compatible Claude interface) + `node` 18+ for the skill's helper scripts + [`ldcli`](https://github.com/launchdarkly/ldcli) (the skill installs it if missing).

## Installation

### CLI (from source)

```bash
go build -o statsig-to-ld .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-to-ld/cmd.version=1.0.0" \
  -o statsig-to-ld .
```

### CLI (pre-built binary)

Download from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page.

### Skill (for Path A — SDK code rewrite)

The skill is bundled in this repo at `skills/statsig-to-launchdarkly-migrator/`. To make it available to Claude Code in any project, symlink or copy it into `~/.claude/skills/`:

```bash
mkdir -p ~/.claude/skills/
ln -s "$(pwd)/skills/statsig-to-launchdarkly-migrator" ~/.claude/skills/statsig-to-launchdarkly-migrator
# or, to install a snapshot:
cp -R skills/statsig-to-launchdarkly-migrator ~/.claude/skills/
```

If you previously curl-installed the legacy agent file from `statsig-to-ld-agent`, it now points at the skill — no action needed on your side.

## Manual quick start

For users running the CLI directly without an agent. (For agent-driven, see [Quick start (Claude Code)](#quick-start-claude-code).)

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

# 7. For SDK code rewrites, launch Claude Code in your project and load
#    skills/statsig-to-launchdarkly-migrator/SKILL.md. Or read the migration
#    playbook to do it by hand.
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

Two AI surfaces live in this repo:

1. **CLI-driving surfaces** — for `analyze`, `flags import`, `targeting import`, `metrics convert`. Backed by the agent-agnostic [`AGENTS.md`](AGENTS.md) (build, API-key handling, recommended sequence, per-subcommand usage, report analysis, troubleshooting). Treat it as authoritative.
2. **SDK-rewrite skill** — for the application-code rewrite step (Statsig SDK calls → LaunchDarkly SDK calls). Lives at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md). Standalone Claude Code skill with progressive-disclosure references, helper scripts, and eval harnesses; it is **not** a shim over `AGENTS.md`.

The CLI-driving surfaces each `@`-import `AGENTS.md`, so a change there propagates to every agent:

| Agent | File | How it loads |
|---|---|---|
| **Codex** + any agent reading the `AGENTS.md` convention | [`AGENTS.md`](AGENTS.md) | OpenAI Codex auto-loads `AGENTS.md` from the repo root as part of its system prompt; other agents that follow the convention (recent Cursor, Sourcegraph, etc.) do the same. Point any other agent at it manually. |
| **Cursor** | [`.cursor/rules/statsig-to-ld.mdc`](.cursor/rules/statsig-to-ld.mdc) | Auto-attaches when the conversation matches the rule's description (Statsig→LD migration topics). |
| **GitHub Copilot** | [`.github/copilot-instructions.md`](.github/copilot-instructions.md) | Auto-loaded into every Copilot Chat session in this repo. |
| **Aider** | [`.aider.conf.yml`](.aider.conf.yml) | Project config `read:` list auto-loads `AGENTS.md` as read-only context for every Aider session in this repo. |
| **Claude Code** (skill) | [`.claude/skills/statsig-to-ld/SKILL.md`](.claude/skills/statsig-to-ld/SKILL.md) | Auto-loads on trigger phrases (subcommand names, API-key env vars, report filenames). |
| **Claude Code** (subagent) | [`.claude/agents/statsig-to-ld.md`](.claude/agents/statsig-to-ld.md) | Invoke via the Task tool for a delegated end-to-end migration in a separate context. |
| **Claude Code** (SDK-rewrite skill) | [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) | Symlink/copy into `~/.claude/skills/`; auto-loads when the conversation mentions migrating Statsig SDK code to LaunchDarkly. **Different concern** from the table above — handles application-code rewrites, not the CLI. |

If your agent isn't listed, point it at `AGENTS.md` (for the CLI) or `skills/statsig-to-launchdarkly-migrator/SKILL.md` (for SDK rewrites) directly.

## See also

- [`AGENTS.md`](AGENTS.md) — operator guide for AI agents driving the CLI
- [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) — Claude Code skill for Path A (SDK code rewrites)
- [`docs/migration-playbook.md`](docs/migration-playbook.md) — what neither tool does (layers, experiments, holdouts, segment recreation, cutover, rollback)
- [`CHANGELOG.md`](CHANGELOG.md)
- [`CONTRIBUTING.md`](CONTRIBUTING.md)
- [`SECURITY.md`](SECURITY.md)
