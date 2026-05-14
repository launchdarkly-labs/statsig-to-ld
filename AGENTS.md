# statsig-to-ld — Operator Guide for AI Agents

This file is the agent-agnostic operator guide for the `statsig-to-ld` CLI, which migrates flag and metric definitions and targeting rules from Statsig to LaunchDarkly. Any AI agent driving this tool — Claude Code, Codex, Cursor, or anything else — should treat the instructions below as authoritative.

If you are an agent reading this, your job is to help users build the tool, scope migrations, run imports phase by phase, interpret reports, and troubleshoot.

## Scope: what the CLI does and doesn't do

The CLI moves **definitions** from Statsig to LaunchDarkly:

| Subcommand | What it does | Writes to |
|---|---|---|
| `analyze` | Read-only Statsig project survey + sizing report | Nothing |
| `flags import` | Create LD flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| `targeting import` | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| `metrics convert` | Convert Statsig metric definitions | LaunchDarkly |

It does **not** modify application code, set up LD experiments, recreate Statsig segments, or migrate layers/holdouts. See `docs/migration-playbook.md` in the repo for what the user still needs to do themselves.

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

```bash
# 1. Scope: how much work, what won't import faithfully
./statsig-to-ld analyze --ld-project my-project

# 2. Flag shells (off in every env — no production impact)
./statsig-to-ld flags import --all --dry-run --ld-project my-project
./statsig-to-ld flags import --all --ld-project my-project

# 3. Targeting (fail-closed by default — preview first)
./statsig-to-ld targeting import --all --dry-run --ld-project my-project
./statsig-to-ld targeting import --all --ld-project my-project

# 4. Metrics (most manual cleanup — do after flags + targeting are validated)
./statsig-to-ld metrics convert --all --dry-run
./statsig-to-ld metrics convert --all --ld-project my-project
```

Re-running any subcommand is safe — existing LD resources are detected by sanitized key and skipped.

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
| `no LD data source specified` | Medium | WH Native metric created without source binding. Use `--ld-data-source` or `--source-mapping`. |

## Analyzing reports

Every subcommand writes a structured JSON report:

| Subcommand | Default report path |
|---|---|
| `analyze` | stdout table; `--output` to write JSON |
| `flags import` | `flag-import-report.json` |
| `targeting import` | `targeting-import-report.json` |
| `metrics convert` | `migration-report.json` |

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
5. **Warehouse Native metrics?** If yes, need `--ld-data-source` or `--source-mapping`.
6. **Custom unit types?** If yes, need `--unit-type-mapping`.
7. **Numeric metric units?** If known, use `--default-unit` to set a meaningful label (default is `"units"`).
8. **EU/FedRAMP?** If yes, need `--ld-url https://app.eu.launchdarkly.com`.

## Output

After every run, report to the user:

1. **Summary counts** — converted/imported, with-warnings, skipped (broken down by reason), failed.
2. **High-severity issues** — `DATA LOSS` (metrics), `skipped_lossy` (targeting), `failed` (any).
3. **Manual follow-ups** — segments that need recreating in LD, gate prerequisites to wire up, units to set, env-mapping warnings.
4. **Next phase** — what to run next in the migration sequence, or a pointer to `docs/migration-playbook.md` for the non-CLI work (SDK call-site rewrites, segment recreation, validation, cutover).
