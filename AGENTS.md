# statsig-to-ld — Operator Guide for AI Agents

This file is the agent-agnostic operator guide for the `statsig-to-ld` CLI, which migrates flag and metric definitions, targeting rules, and warehouse-native experimentation from Statsig to LaunchDarkly. Any AI agent driving this tool — Claude Code, Codex, Cursor, or anything else — should treat the instructions below as authoritative.

If you are an agent reading this, your job is to help users build the tool, scope migrations, run imports phase by phase, interpret reports, and troubleshoot.

## Companion surfaces

This repo ships three surfaces; this file covers the CLI half. If you landed here from a search, the other two are:

- **[`README.md`](README.md)** — top-level entry point with the path-selector ("Agent Instructions") an orchestrator agent uses to decide which surface to invoke for a given user request, plus credential handling and install steps.
- **[`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md)** — the Claude Code skill that rewrites Statsig SDK calls → LaunchDarkly SDK calls in application code. **Use the skill, not the CLI, for SDK call-site rewrites.** Standalone — not a shim over this file.
- **[`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md)** — Claude Code subagent for the `warehouse` subcommand specifically. Has the full wizard / SQL / resume detail that's only summarized here.

## Scope: what the CLI does and doesn't do

The CLI moves **definitions** from Statsig to LaunchDarkly:

| Subcommand | What it does | Writes to |
|---|---|---|
| `analyze` | Read-only Statsig project survey + sizing report | Nothing |
| `flags import` | Create LD flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| `targeting import` | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| `metrics convert` | Convert Statsig metric definitions | LaunchDarkly |
| `warehouse` | Set up the LaunchDarkly side of a Statsig warehouse-native project: integrations + LD metric data sources + `source-mapping.json` for the metric-definitions handoff. **Does not migrate metric definitions** — that's `metrics convert`. | LaunchDarkly + warehouse |

It does **not** modify application code (for that, use the [SDK-rewrite skill](skills/statsig-to-launchdarkly-migrator/SKILL.md)), set up LD experiments, recreate Statsig segments, or migrate layers/holdouts. See [`docs/migration-playbook.md`](docs/migration-playbook.md) in the repo for what the user still needs to do themselves.

## Tool Location and Setup

The CLI source is in this repository. Requires **Go 1.25 or higher** (macOS or Linux). Build it before first use:

```bash
go build -o statsig-to-ld .
```

The binary is `./statsig-to-ld`. All commands below assume you are in the repository root. Building from source with `go build` is the recommended path; the tagged release binaries are mainly for Linux or for major collections of updates and bug fixes.

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

Earlier phases are read-only or build the runtime layer; metrics go last because they're the most likely to need manual cleanup (DATA LOSS warnings, unsupported metric types), and doing them after flags + targeting are validated avoids reworking orphan metrics.

If the user is also rewriting application code from the Statsig SDK to the LaunchDarkly SDK, that's the SDK-rewrite skill's job — invoke it as **step 0** (it emits a `migration-summary.json` with canonical flag keys that `flags import` keys off). If they aren't, skip step 0.

```bash
# 0. (Conditional) SDK call-site rewrites — invoke the skill, not the CLI.
#    See skills/statsig-to-launchdarkly-migrator/SKILL.md.

# 1. Scope: how much work, what won't import faithfully
./statsig-to-ld analyze --ld-project my-project

# 2. Flag shells (off in every env — no production impact)
./statsig-to-ld flags import --all --dry-run --ld-project my-project
./statsig-to-ld flags import --all --ld-project my-project

# 3. Targeting (fail-closed by default — preview first)
./statsig-to-ld targeting import --all --dry-run --ld-project my-project
./statsig-to-ld targeting import --all --ld-project my-project

# 4. (Conditional) Warehouse-native setup — sets up integrations + data sources
#    and writes source-mapping.json. See decision tree below.
./statsig-to-ld warehouse --statsig-key ... --dry-run
./statsig-to-ld warehouse --statsig-key ... --ld-project my-project --ld-environment production

# 5. Metric definitions. If step 4 ran, pass --source-mapping so warehouse-native
#    metrics bind to the data sources warehouse just created.
./statsig-to-ld metrics convert --all --dry-run
./statsig-to-ld metrics convert --all --ld-project my-project \
  [--source-mapping source-mapping.json]   # add this line if step 4 ran
```

### When to run step 4 (`warehouse`) and how it feeds step 5 (`metrics convert`)

`warehouse` handles only the parts unique to warehouse-native (interactive integrations wizard, SQL setup, LD data source creation with column-schema discovery). `metrics convert` handles **all** metric definitions — event-based and warehouse-native alike. The handoff between them is `source-mapping.json`, which `warehouse` writes (Statsig metric source name → LD data source key).

| Statsig usage | What to run | Why |
|---|---|---|
| Warehouse-native, **LD data sources already exist** (set up in the LD UI, via Terraform, or provisioned for the account — the **Figma** case) | **Skip `warehouse`.** `metrics convert` (step 5) with `--ld-data-source <key>` (one source for all) or `--source-mapping source-mapping.json` (per-source), supplied by you. | Nothing to create in LD, so `warehouse` has no job. You just tell `metrics convert` which existing data source key to bind each warehouse-native metric to. See the [`metrics convert` Warehouse Native section](#warehouse-native) for the JSON shape. |
| Warehouse-native, data sources **do not** exist yet | `warehouse` (step 4) → `metrics convert --source-mapping source-mapping.json` (step 5). | `warehouse` creates the integrations + data sources but **not** the metrics, and writes the `source-mapping.json`. `metrics convert` then creates every metric definition and binds the warehouse-native ones to the data sources via the mapping. |
| Event-based metrics only (Statsig Cloud, no warehouse-native) | `metrics convert` (step 5). Skip `warehouse` (step 4). | `warehouse` would have nothing to do — there are no data sources to create. |
| Mixed (both) | Same as warehouse-native: `warehouse` (step 4, unless data sources already exist) → `metrics convert --source-mapping source-mapping.json` (step 5). | `metrics convert` walks all metrics; event-based ones don't need a data source binding, warehouse-native ones do — the mapping file resolves both in one pass. |

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
# List available metric names/types, then exit (only the Statsig key needed)
./statsig-to-ld metrics convert --list

# Dry-run preview (only Statsig key needed)
./statsig-to-ld metrics convert --all --dry-run

# Bulk convert
./statsig-to-ld metrics convert --all --ld-project my-project

# Single metric (get the exact name from --list first)
./statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

# Incremental migration (safest types first)
./statsig-to-ld metrics convert --all --include-types event_count_custom,sum \
  --ld-project my-project
./statsig-to-ld metrics convert --all --include-types mean,event_user \
  --ld-project my-project
./statsig-to-ld metrics convert --all --ld-project my-project
```

`--ld-project` also reads the `LD_PROJECT` environment variable, so you can `export LD_PROJECT=my-project` once instead of passing the flag on every run.

By default, metrics whose conversion would be **lossy** (a Statsig feature dropped or approximated — event filters, per-unit capping, log transform, daily participation rate, count-distinct, metadata aggregation, or extra metric events) are **skipped** and recorded as `skipped_lossy` in the report. Add `--convert-lossy` to convert them anyway and accept the imperfect result:

```bash
./statsig-to-ld metrics convert --all --ld-project my-project --convert-lossy
```

### Warehouse Native

Warehouse-native metrics must bind to an LD metric **data source**. If you ran `warehouse` (step 4), pass the `source-mapping.json` it wrote. **If the data sources already exist** (LD UI, Terraform, or provisioned for the account — e.g. Figma), skip `warehouse` and supply the binding here yourself, either as a single default or a per-source mapping you hand-write.

```bash
# Single source for all metrics — every warehouse-native metric binds to this key
./statsig-to-ld metrics convert --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Per-source mapping — hand-written, same format warehouse would have produced:
# Statsig metric source name -> existing LD data source key
cat > source-mapping.json << 'EOF'
{
  "purchases_table": "snowflake-purchases-ds",
  "sessions_table": "snowflake-sessions-ds"
}
EOF
./statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping source-mapping.json
```

The keys are each metric's `metricSourceName` (from the Statsig metrics API / console; a `warehouse --dry-run` also writes every source name to its export file); the values are the keys of the existing LD data sources. A warehouse-native metric resolved by neither flag is created without a data source binding (a `no LD data source specified` warning), and ratio metrics are rejected by LD without one.

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
| `ratio` | custom + denominator | Supported — requires a warehouse data source (`--ld-data-source` / `--source-mapping`) |
| `funnel` | — | Not converted — would need an LD metric group |
| `composite` | — | No LD equivalent |
| `percentile` | — | LD uses percentile as analysisType, not metric type |

Windowed metrics (`event_user_window`, or a custom rollup window) and winsorization convert too — winsorization needs a numeric metric, and a custom window needs a warehouse data source (otherwise it's dropped; see below).

### Metric warnings to surface to the user

Many of these mark a conversion **lossy**: by default the metric is skipped (`skipped_lossy` in the report) with the warning as the reason, and `--convert-lossy` converts it anyway. Advisory warnings (the unit-type nudge, a truncated key) do **not** cause a skip.

| Warning | Severity | What to do |
|---|---|---|
| `DATA LOSS: ... filter criteria` | High | Lossy — skipped by default. The LD metric would match ALL events, not just the filtered subset; set the filters up manually in LD, or `--convert-lossy` to accept the loss. |
| `N metric events — only the first is used` | Medium | Lossy — only the first event is used; extra events are dropped. |
| `winsorization ... occurrence metric` | Low | Lossy — LD can't winsorize an occurrence metric (numeric metrics winsorize fine). |
| `per-unit capping` | Low | Lossy — per-unit cap not applied. |
| `custom rollup window` | Low | Lossy only when no data source is bound; pass `--ld-data-source` (snowflake) to apply the window. |
| `unitType ... may not match an LD context kind` | Medium | Use `--unit-type-mapping` to map explicitly. |
| `no LD data source specified` | Medium | Warehouse-native metric is being created without a data source binding. Fix: run `statsig-to-ld warehouse` first (it creates the data sources and writes `source-mapping.json`), then re-run `metrics convert --source-mapping source-mapping.json`. If the data sources already exist (set up by hand or via Terraform), pass `--ld-data-source` or `--source-mapping` directly. |

## Subcommand: warehouse

Sets up the LaunchDarkly side of a Statsig warehouse-native experimentation project: data export integration, experimentation integration, and LD metric data sources. **It does not migrate metric definitions** — `metrics convert` does that, using the `source-mapping.json` this subcommand writes. Full operator detail (interactive SQL wizards per warehouse type, resume semantics, the `migration_state.json` lifecycle) is in [`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md); this section is enough to drive the basic flow and decide when to run it.

```bash
# Dry-run from live Statsig API (only Statsig key needed)
./statsig-to-ld warehouse --statsig-key console-... --dry-run

# Full warehouse setup (integrations + data sources)
./statsig-to-ld warehouse \
  --statsig-key console-... --ld-key api-... \
  --ld-project my-project --ld-environment production

# Then migrate metric definitions, binding warehouse-native ones to the data sources
./statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping source-mapping.json

# From an export file (no Statsig key needed)
./statsig-to-ld warehouse \
  --ld-key api-... --ld-project my-project --ld-environment production \
  --statsig-export-file statsig_export_2026-05-13_120000.json

# Resume after a failure (loads migration_state.json)
./statsig-to-ld warehouse ... --resume

# Phase 2 only — set up integrations, stop before creating data sources
./statsig-to-ld warehouse ... --only warehouse

# Phase 3 only — skip integrations wizard (assumes integrations exist), create data sources
./statsig-to-ld warehouse ... --only data-sources
```

### Phases

1. **Export** — Fetches `wh_connections` and `metric_source/list` from Statsig (or loads from `--statsig-export-file`). Writes `statsig_export_<timestamp>.json`. (Metric definitions are not fetched here — `metrics convert` re-fetches them itself.)
2. **Warehouse setup** (interactive) — Checks for existing data-export and experimentation integrations in LD; if absent, runs the wizard. Snowflake / BigQuery / Databricks / Redshift each have their own setup path. Auto-skips if integrations already exist.
3. **Data sources** — Creates LD data sources (calling the warehouse preview API to discover real column schemas first), then writes `source-mapping.json` mapping each Statsig metric source name to the LD data source key it created. The subcommand prints the recommended `metrics convert --source-mapping source-mapping.json` hand-off command at the end of a successful run.

### Relationship to `metrics convert`

`warehouse` and `metrics convert` are **separate, complementary** subcommands. `warehouse` handles only the parts unique to warehouse-native (integrations + data sources). `metrics convert` handles **all** metric definitions, event-based and warehouse-native alike — for warehouse-native, it binds each metric to an existing LD data source by key.

The two subcommands compose via `source-mapping.json`:

- `warehouse` writes `source-mapping.json` (Statsig metric source name → LD data source key).
- `metrics convert --source-mapping source-mapping.json` reads it and binds each warehouse-native metric to the correct data source.

The decision tree is in the [migration-sequence table above](#when-to-run-step-4-warehouse-and-how-it-feeds-step-5-metrics-convert). The `--ld-data-source` / `--source-mapping` flags on `metrics convert` are also available for users who skip `warehouse` entirely and pre-create their data sources by hand or via Terraform.

For event-based-only Statsig projects (no warehouse-native), skip `warehouse` entirely — `metrics convert` doesn't need a data source binding for event-based metrics.

### Internal API endpoints

The metric data source CRUD operations use LaunchDarkly's `/internal/` API endpoints. These accept API key auth but are not part of the public API and may change.

## Analyzing reports

Every subcommand writes a structured JSON report:

| Subcommand | Default report path |
|---|---|
| `analyze` | stdout table; `--output` to write JSON |
| `flags import` | `flag-import-report.json` |
| `targeting import` | `targeting-import-report.json` |
| `metrics convert` | `migration-report.json` |
| `warehouse` | `statsig_export_<timestamp>.json` (Phase 1 export) + `migration_state.json` (Phase 2/3 progress, for `--resume`) |

Useful `jq` queries:

```bash
# metrics convert: summary counts
cat migration-report.json | jq '{total: .statsig_metrics_total, dry_run, converted, with_warnings: .converted_with_warnings, skipped_existing, skipped_incompatible, skipped_lossy, failed}'

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
LaunchDarkly rate-limiting. Throttled requests are retried automatically, but a very large project can still exhaust the retries. The default `--concurrency` is 4 (deliberately conservative); if you still see 429s, lower it further (e.g. `--concurrency 2`). Re-running is safe — already-created metrics are skipped (`E` in the progress line), so only the throttled ones are retried.

### "metric not found among N Statsig metrics"
`--metric` requires an exact name match. Run `metrics convert --list` to print the available metric names and types (Statsig key only), or `--all --dry-run` to preview full conversions in the report.

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

1. **API keys set?** User should set `STATSIG_CONSOLE_KEY` and `LD_API_KEY` env vars (and optionally `LD_PROJECT`, so `--ld-project` isn't needed on every run).
2. **LD project key?** The `--ld-project` value. Required for everything except a Statsig-only `analyze` or `metrics convert --dry-run`.
3. **Migration scope?** All gates + dynamic configs + metrics, or a subset (via `--import-type`, `--include-tag`, `--include-types`, `--metric`)?
4. **Lossy targeting?** Run `analyze` first; if there are lossy sources the user wants to import, decide which `--accept-data-loss` features they'll accept.
5. **Warehouse-native experimentation?** If yes, you'll run **two** subcommands: `warehouse` first (sets up integrations + data sources, writes `source-mapping.json`), then `metrics convert --source-mapping source-mapping.json` to create the metric definitions bound to those data sources. Confirm warehouse type (Snowflake / BigQuery / Databricks / Redshift) and the LD environment key (`--ld-environment`). If the LD integrations and data sources already exist (set up by hand or via Terraform), skip `warehouse` and pass `--ld-data-source` or `--source-mapping` directly to `metrics convert`.
6. **Custom unit types?** If yes, need `--unit-type-mapping`.
7. **Numeric metric units?** If known, use `--default-unit` to set a meaningful label (default is `"units"`).
8. **EU/FedRAMP?** If yes, need `--ld-url https://app.eu.launchdarkly.com`.
9. **SDK call-site rewrites?** Not handled by this CLI. If the user is migrating application code (Statsig SDK → LD SDK) for JS / TS / React / Node, point them at the [SDK-rewrite skill](skills/statsig-to-launchdarkly-migrator/SKILL.md). For other languages, point them at [`docs/migration-playbook.md`](docs/migration-playbook.md) §1.

## Output

After every run, report to the user:

1. **Summary counts** — converted/imported, with-warnings, skipped (broken down by reason), failed.
2. **High-severity issues** — `DATA LOSS` (metrics), `skipped_lossy` (targeting), `failed` (any).
3. **Manual follow-ups** — segments that need recreating in LD, gate prerequisites to wire up, units to set, env-mapping warnings.
4. **Next phase** — what to run next in the migration sequence:
   - If they ran `analyze` → `flags import`.
   - If they ran `flags import` → `targeting import`.
   - If they ran `targeting import` → `warehouse` (if warehouse-native) or `metrics convert`.
   - If they ran `warehouse` and have remaining event-based metrics → `metrics convert`.
   - If they're done with CLI subcommands and still need application-code rewrites → the [SDK-rewrite skill](skills/statsig-to-launchdarkly-migrator/SKILL.md).
   - For the rest (segment recreation, experiments, validation, cutover, rollback) → [`docs/migration-playbook.md`](docs/migration-playbook.md).
