# statsig-to-ld — Operator Guide for AI Agents

This file is the agent-agnostic operator guide for the `statsig-to-ld` CLI, which migrates flag and metric definitions, targeting rules, and warehouse-native experimentation from Statsig to LaunchDarkly. Any AI agent driving this tool — Claude Code, Codex, Cursor, or anything else — should treat the instructions below as authoritative.

If you are an agent reading this, your job is to help users build the tool, scope migrations, run imports phase by phase, interpret reports, and troubleshoot.

**Companion surfaces (read these alongside this file if relevant to the user's request):**
- [`README.md`](README.md) — the top-level **Agent Instructions** bootstrap that orchestrates this CLI together with the SDK-rewrite skill. If a user asks the agent to "help me migrate from Statsig to LaunchDarkly" (without naming a subcommand), start from the README's path selector, not this file directly.
- [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) — Claude Code skill for the **SDK call-site rewrite** in application code (JavaScript / TypeScript / React / Node.js). This file (AGENTS.md) does not cover that step.
- [`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md) — detailed operator guide for the `warehouse` subcommand (wizard prompts, SQL setup, resume semantics). This file links to that file for the warehouse-specific detail rather than duplicating it.

## Scope: what the CLI does and doesn't do

The CLI moves **definitions** from Statsig to LaunchDarkly:

| Subcommand | What it does | Writes to |
|---|---|---|
| `analyze` | Read-only Statsig project survey + sizing report | Nothing |
| `flags import` | Create LD flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| `targeting import` | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| `metrics convert` | Convert Statsig metric definitions | LaunchDarkly |
| `warehouse` | Migrate Statsig warehouse-native experimentation (integrations + data sources + warehouse-native metrics) | LaunchDarkly + warehouse |

It does **not** modify application code, set up LD experiments, recreate Statsig segments, or migrate layers/holdouts.

- For **application-code SDK rewrites** (Statsig SDK calls → LD SDK calls), use the bundled Claude Code skill at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md). That's the skill the README bootstrap calls "Path A."
- For **everything else** the migration needs (segment recreation, layers, experiment setup, validation, cutover, rollback), see [`docs/migration-playbook.md`](docs/migration-playbook.md).

## Tool Location and Setup

The CLI source is in this repository. Build it before first use:

```bash
go build -o statsig-to-ld .
```

The binary is `./statsig-to-ld`. All commands below assume you are in the repository root.

## API Key Setup

The tool requires two API keys:
- **Statsig Console API key** — starts with `console-`
- **LaunchDarkly API access token** — starts with `api-`, needs the Writer role (or read/write on flags + metrics + environments) in the target project

### Before running the tool

Ask the user to provide their API keys using one of these methods (listed from most to least secure):

1. **Run the tool manually with interactive prompts** (most secure — keys never saved anywhere):
   Tell the user to run the command themselves in their terminal. When no keys are provided via flags or env vars, the tool prompts with echo disabled. Keys never touch disk, shell history, or process listings. This method does not work when an AI agent runs the tool, since the agent's shell is non-interactive.

2. **Set environment variables before starting the agent session** (recommended for agent use):
   The user should run these exports in their terminal **before** launching the agent session, so the agent's subprocess inherits them:
   ```bash
   read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
   read -rs LD_API_KEY && export LD_API_KEY
   ```
   Using `read -rs` keeps the value out of shell history. If the user exports these *after* the agent session has started, the agent's shell will not see them — they'd need to restart the agent session.

3. **Pass keys as command-line flags** (least secure — visible in shell history and `ps` output):
   ```bash
   ./statsig-to-ld <subcommand> --statsig-key console-xxx --ld-key api-xxx --ld-project my-project
   ```
   Acceptable for short-lived staging tokens or CI/CD where keys are injected from a secrets manager.

### When running commands as an agent

If the user has set environment variables, omit key flags from all commands — the tool picks them up automatically. If the user asks you to pass keys directly, use `--statsig-key` and `--ld-key` flags.

**Never ask the user to paste API keys into the chat.** Direct them to set env vars or pass flags instead.

## Recommended migration sequence

Always run the phases in this order. Earlier phases are read-only or build the runtime layer; metrics go last because they're the most likely to need manual cleanup (DATA LOSS warnings, unsupported metric types), and doing them after flags + targeting are validated avoids reworking orphan metrics.

If the user is also rewriting application SDK code, **the SDK skill (Path A in the README bootstrap) runs first**, before any subcommand below — its `migration-summary.json` is the canonical flag-key list that `flags import` matches.

```bash
# 1. Scope: how much work, what won't import faithfully
./statsig-to-ld analyze --ld-project my-project

# 2. Flag shells (off in every env — no production impact)
./statsig-to-ld flags import --all --dry-run --ld-project my-project
./statsig-to-ld flags import --all --ld-project my-project

# 3. Targeting (fail-closed by default — preview first)
./statsig-to-ld targeting import --all --dry-run --ld-project my-project
./statsig-to-ld targeting import --all --ld-project my-project

# 4. Warehouse-native experimentation (ONLY if the user has warehouse-native metrics in Statsig)
#    Full pipeline in one pass: integration setup + data sources + warehouse-native metrics, all bound.
#    See .claude/agents/statsig-warehouse-migrator.md for the wizard flow.
./statsig-to-ld warehouse --dry-run
./statsig-to-ld warehouse --ld-project my-project --ld-environment production

# 5. Metrics convert — ONLY for event-based metrics (and only if step 4 didn't already cover everything)
#    metrics convert walks ALL Statsig metrics. On a mixed project (some warehouse-native, some
#    event-based), step 4 created the warehouse-native ones; this step idempotently skip-exists those
#    and creates the event-based ones.
./statsig-to-ld metrics convert --all --dry-run
./statsig-to-ld metrics convert --all --ld-project my-project
```

Re-running any subcommand is safe — existing LD resources are detected by sanitized key and skipped.

**When step 4 vs step 5 (or both) is correct.** Use `analyze` to find out which kinds of metrics the Statsig project has:

| Statsig project has | Run step 4 (`warehouse`)? | Run step 5 (`metrics convert`)? |
|---|---|---|
| Only event-based metrics | No | Yes |
| Only warehouse-native metrics | Yes | No — `warehouse` already created them |
| Both (mixed) | Yes, first | Yes, after — idempotent skip-exists handles the warehouse-native ones |

## Subcommand: analyze

Read-only sizing report. Surveys gates, dynamic configs, environments, and metrics and tells the user what will import faithfully, what will be lossy, and what will be skipped. Writes nothing.

```bash
# Statsig-only — no LD account needed yet
./statsig-to-ld analyze --statsig-key console-...

# Full analysis with env-mapping preview
./statsig-to-ld analyze --ld-project my-project

# Save structured JSON alongside the table
./statsig-to-ld analyze --ld-project my-project --output analyze.json
```

Use this before every migration. The report is the basis for deciding which `--accept-data-loss` opt-ins (if any) the user wants for `targeting import`.

## Subcommand: flags import

Creates LD **flag shells** from Statsig gates and dynamic configs. Variations, default values, tags, and maintainer are set; per-environment targeting is **not** — `targeting import` handles that next.

```bash
# Dry-run first
./statsig-to-ld flags import --all --dry-run --ld-project my-project

# Import everything (gates + dynamic configs)
./statsig-to-ld flags import --all --ld-project my-project

# Gates only, filtered by tag
./statsig-to-ld flags import --all --import-type gates --include-tag p0 \
  --ld-project my-project

# Custom LD tag for traceability
./statsig-to-ld flags import --all --ld-tag from-statsig-2026-may \
  --ld-project my-project
```

Created flags are tagged `imported-from-statsig` by default (configurable via `--ld-tag`) so `targeting import` and re-runs can find them.

**Idempotency**: dedupe is by sanitized LD key, not display name. Renaming a Statsig gate between runs does NOT create a duplicate.

## Subcommand: targeting import

Applies per-environment targeting (rules, rollouts, user/context targets, overrides) to flag shells previously created by `flags import`. Reconciles Statsig envs to LD envs by case-insensitive name; auto-creates missing LD envs (turn off with `--no-create-envs`).

**Fail-closed by default**: sources whose targeting cannot be faithfully reproduced are SKIPPED with a `skipped_lossy` entry in the report. The lossy features are:

| Feature | Why it's lossy |
|---|---|
| `passes_segment` / `fails_segment` | Statsig segments aren't auto-recreated in LD; the condition is dropped. |
| `passes_gate` / `fails_gate` | Gate prerequisites aren't auto-recreated; the condition is dropped. |
| Custom `unit_id` (non-`userID`) | Targeting is squashed to LD's `user` context kind in v1. |
| Multi-variant DC overrides | Statsig overrides are binary pass/fail; multi-variant fidelity is lost. |
| Unreachable trailing rules | Rules after a public/match-everyone rule are dropped. |

```bash
# Strict (default): skip lossy flags
./statsig-to-ld targeting import --all --ld-project my-project

# Accept all lossy features
./statsig-to-ld targeting import --all --accept-data-loss=all \
  --ld-project my-project

# Accept only specific features
./statsig-to-ld targeting import --all \
  --accept-data-loss=segments,unreachable_rules \
  --ld-project my-project

# Don't auto-create LD envs (mark missing ones unreachable)
./statsig-to-ld targeting import --all --no-create-envs --ld-project my-project
```

The accepted `--accept-data-loss` values are: `segments`, `prerequisites`, `custom_unit_id`, `unreachable_rules`, `multi_variant_overrides`, or `all`.

**Re-run caveat**: `targeting import` overwrites per-env settings on every matching flag. Hand-tuned LD UI edits made after the first import will be overwritten on re-run.

## Subcommand: metrics convert

Converts Statsig metric definitions into LaunchDarkly metrics. Supports Statsig Cloud and Warehouse Native, with idempotent re-runs and parallel processing.

```bash
# Dry-run preview (only Statsig key needed)
./statsig-to-ld metrics convert --all --dry-run

# Bulk convert
./statsig-to-ld metrics convert --all --ld-project my-project

# Single metric
./statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

# Incremental migration (safest types first)
./statsig-to-ld metrics convert --all --include-types event_count_custom,sum \
  --ld-project my-project
./statsig-to-ld metrics convert --all --include-types mean,event_user \
  --ld-project my-project
./statsig-to-ld metrics convert --all --ld-project my-project
```

### Warehouse Native

```bash
# Single source for all metrics
./statsig-to-ld metrics convert --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Per-source mapping
cat > sources.json << 'EOF'
{"purchases_table": "snowflake-purchases-ds", "sessions_table": "snowflake-sessions-ds"}
EOF
./statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping sources.json
```

### Custom unit types (company-level experiments)

```bash
cat > unit-types.json << 'EOF'
{"companyID": "company", "teamID": "team"}
EOF
./statsig-to-ld metrics convert --all --ld-project my-project \
  --unit-type-mapping unit-types.json
```

Without this mapping, non-`userID` unit types are lowercased and a warning is emitted.

### Metric type conversion

| Statsig type | LD kind | Status |
|---|---|---|
| `event_count_custom` | custom | Supported |
| `sum` | custom (numeric, sum) | Supported |
| `mean` | custom (numeric, average) | Supported |
| `event_user` | custom | Supported |
| `event_user_window` | custom | Supported |
| `ratio` | — | Not yet supported in LD |
| `funnel` | — | Requires LD metric group |
| `composite` | — | No LD equivalent |
| `percentile` | — | LD uses percentile as analysisType, not metric type |

### Metric warnings to surface to the user

| Warning | Severity | What to do |
|---|---|---|
| `DATA LOSS: ... filter criteria` | High | LD metric matches ALL events, not just the filtered subset. Review dropped filters and set up manually in LD. |
| `N metric events — only the first is used` | Medium | Multi-event metrics only use the first event. |
| `winsorization ... not yet supported` | Low | Outlier clipping not applied. |
| `per-unit capping` | Low | Daily cap not applied. |
| `custom rollup window` | Low | LD uses full experiment duration. |
| `unitType ... may not match an LD context kind` | Medium | Use `--unit-type-mapping` to map explicitly. |
| `no LD data source specified` | Medium | WH Native metric created without source binding. Either run `warehouse` first (full pipeline that creates data sources and binds metrics), or pre-create data sources in LD and pass `--ld-data-source` / `--source-mapping`. |

## Subcommand: warehouse

Migrates Statsig **warehouse-native experimentation** to LaunchDarkly: sets up the warehouse integration in LD (Snowflake / BigQuery / Databricks / Redshift), creates LD metric data sources, and creates warehouse-native LD metrics bound to those data sources — all in one pass.

This subcommand has its own detailed operator guide at [`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md) — wizard prompts, SQL scripts, resume semantics, and per-flag detail live there. The summary below is enough to decide whether the user needs this subcommand and to drive the basic flow; defer to the shim for any warehouse-specific question.

```bash
# Dry-run preview (writes statsig_export_*.json with what would migrate)
./statsig-to-ld warehouse --dry-run

# Full migration (live Statsig export → integration wizard → data sources + metrics in LD)
./statsig-to-ld warehouse \
  --ld-project my-project --ld-environment production

# From a previously exported file (no Statsig key needed)
./statsig-to-ld warehouse \
  --ld-project my-project --ld-environment production \
  --statsig-export-file statsig_export_2026-05-13_120000.json

# Resume a partially-completed migration
./statsig-to-ld warehouse ... --resume

# Scope a re-run
./statsig-to-ld warehouse ... --only data-sources    # or --only metrics
```

**Three phases**: (1) export from Statsig, (2) interactive integration setup in LD with warehouse-specific wizards, (3) data sources + warehouse-native metrics. Progress is checkpointed to `migration_state.json` so `--resume` picks up where a failure left off.

**Relationship to `metrics convert`**: `warehouse` and `metrics convert` overlap on creating warehouse-native LD metrics. `warehouse` is the full pipeline (integration + data sources + metrics, all bound). `metrics convert` only creates the metric and references an existing data source by key. For warehouse-native users, prefer `warehouse`. For projects with both warehouse-native AND event-based metrics, run `warehouse` first, then `metrics convert` (which will idempotently skip the warehouse-native metrics `warehouse` already created and only handle the event-based ones).

**Warehouse-native metric type mapping** (different from `metrics convert`'s event-based mapping above):

| Statsig type | LD mapping | Notes |
|---|---|---|
| `sum` | numeric, unitAggregationType: sum | |
| `mean` | numeric, unitAggregationType: average | |
| `event_count` | numeric, unitAggregationType: sum | |
| `count_distinct` | numeric, unitAggregationType: sum | |
| `percentile` | numeric, analysisType: percentile | `eventDefault` disabled |
| `user` / `user_count` | non-numeric (conversion) | |
| `conversion` | non-numeric (conversion) | |
| `retention` | non-numeric (conversion) | |
| `ratio`, `funnel`, `composite`, `undefined` | — | Skipped — no LD equivalent |

## Analyzing reports

Every subcommand writes a structured JSON report:

| Subcommand | Default report path |
|---|---|
| `analyze` | stdout table; `--output` to write JSON |
| `flags import` | `flag-import-report.json` |
| `targeting import` | `targeting-import-report.json` |
| `metrics convert` | `migration-report.json` |
| `warehouse` | written via internal state (`migration_state.json`) + stdout summary |

Useful `jq` queries:

```bash
# metrics convert: summary counts
cat migration-report.json | jq '{total: .statsig_metrics_total, converted: .converted, with_warnings: .converted_with_warnings, skipped_incompatible: .skipped_incompatible, failed: .failed}'

# metrics convert: DATA LOSS warnings (most critical)
cat migration-report.json | jq '.metrics[] | select(.warnings[]? | contains("DATA LOSS")) | {name: .statsig_name, warnings}'

# targeting import: lossy skips
cat targeting-import-report.json | jq '.flags[] | select(.status == "skipped_lossy") | {key: .flag_key, lossy: .lossy_features}'

# flags import: which sources became which LD keys
cat flag-import-report.json | jq '.flags[] | {statsig_id, ld_key, status}'
```

## EU / FedRAMP

```bash
./statsig-to-ld <subcommand> ... --ld-url https://app.eu.launchdarkly.com
```

All subcommands accept `--ld-url` and `--statsig-url` overrides. URLs must include `https://`.

## Troubleshooting

### "unsupported protocol scheme"
`--ld-url` or `--statsig-url` is missing `https://`. Use `https://example.com`, not `example.com`.

### Many FAIL results with "HTTP 429"
Rate limiting. Lower `--concurrency` (default 10) to 5 or 3.

### "metric not found among N Statsig metrics"
`--metric` requires an exact name match. Run `--all --dry-run` first to see available names in the report.

### All metrics or flags show "skipped_existing"
Already created in a previous run — expected and safe. The tool is idempotent.

### `targeting import` skips a flag with `skipped_lossy`
The Statsig source uses a lossy feature (segments, gate prerequisites, custom unit_id, multi-variant overrides, unreachable rules). Either accept the loss with `--accept-data-loss=...` or address the lossy condition first (recreate the segment in LD, set up an LD flag prerequisite, etc.).

### "key collision" warnings
Two Statsig sources with different names (e.g. `revenue (gross)` and `revenue/gross`) sanitize to the same LD key. Only the first is created. Rename one in Statsig or in LD if both are needed.

### LD environment auto-creation failed
Either the LD token lacks `createEnvironment` permission (run `targeting import --no-create-envs` to skip auto-create) or the env already exists with a different name. Check the report's `notes` field for the specific reason.

## Information to Gather from the User

Before running the tool, confirm:

1. **API keys set?** User should set `STATSIG_CONSOLE_KEY` and `LD_API_KEY` env vars.
2. **LD project key?** The `--ld-project` value. Required for everything except a Statsig-only `analyze` or `metrics convert --dry-run`.
3. **Migration scope?** All gates + dynamic configs + metrics, or a subset (via `--import-type`, `--include-tag`, `--include-types`, `--metric`)?
4. **Lossy targeting?** Run `analyze` first; if there are lossy sources the user wants to import, decide which `--accept-data-loss` features they'll accept.
5. **Warehouse Native metrics?** If yes, prefer the `warehouse` subcommand (full pipeline). Use `metrics convert` with `--ld-data-source` / `--source-mapping` only if data sources already exist in LD (e.g., pre-created manually) and the user wants to skip the integration setup wizard.
6. **Custom unit types?** If yes, need `--unit-type-mapping`.
7. **Numeric metric units?** If known, use `--default-unit` to set a meaningful label (default is `"units"`).
8. **EU/FedRAMP?** If yes, need `--ld-url https://app.eu.launchdarkly.com`.

## Output

After every run, report to the user:

1. **Summary counts** — converted/imported, with-warnings, skipped (broken down by reason), failed.
2. **High-severity issues** — `DATA LOSS` (metrics), `skipped_lossy` (targeting), `failed` (any).
3. **Manual follow-ups** — segments that need recreating in LD, gate prerequisites to wire up, units to set, env-mapping warnings.
4. **Next phase** — what to run next in the migration sequence. Specifically:
   - **For SDK call-site rewrites** (Statsig SDK calls → LD SDK calls in the user's application code), point to [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md). The skill is bundled in this repo; running it in Claude Code (or another Claude interface) does the rewrite.
   - **For everything else** (segment recreation, layer/experiment migration, validation strategy, cutover, rollback), point to [`docs/migration-playbook.md`](docs/migration-playbook.md).
