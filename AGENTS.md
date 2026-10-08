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
| Warehouse-native, **LD data sources already exist** (set up in the LD UI, via Terraform, or provisioned for the account) | **Skip `warehouse`.** `metrics convert` (step 5) with `--ld-data-source <key>` (one source for all) or `--source-mapping source-mapping.json` (per-source), supplied by you. | Nothing to create in LD, so `warehouse` has no job. You just tell `metrics convert` which existing data source key to bind each warehouse-native metric to. See the [`metrics convert` Warehouse Native section](#warehouse-native) for the JSON shape. |
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

# Export every metric's raw Statsig JSON for debugging (Statsig key only)
./statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json

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

> **Warehouse-native and ratio metrics need an LD data source, and a `--dry-run` will not fail without one.** The dry run reports these metrics as converted, but a real run rejects ratio metrics (HTTP 400) and creates the others unbound (they collect no data). Always pass `--ld-data-source <key>` or `--source-mapping <file>` when converting warehouse-native or ratio metrics. When any convert without a data source, the run prints a `⚠ N converted metric(s) resolved no LaunchDarkly data source` line under the summary and flags each one with a `no LD data source specified` warning in the report.

Warehouse-native metrics must bind to an LD metric **data source**. If you ran `warehouse` (step 4), pass the `source-mapping.json` it wrote. **If the data sources already exist** (LD UI, Terraform, or provisioned for the account), skip `warehouse` and supply the binding here yourself, either as a single default or a per-source mapping you hand-write.

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

**Event keys come from the data source.** LaunchDarkly reads only the rows whose event key column equals the metric's event key, and Statsig sources have no event key, so `warehouse` creates each data source with a constant `LD_EVENT_KEY` column holding the data source key (see [The constant event key](docs/cli-reference.md#the-constant-event-key)). When `--ld-data-source` or `--source-mapping` is set, `metrics convert` reads the bound data sources from LaunchDarkly, so **pass `--ld-key` and `--ld-project` on dry runs too**. For each data source that projects the constant (including one whose wrapper was edited in LD but still opens with `SELECT *, '<its key>' AS <its key column> FROM (`), warehouse-native metrics get the data source key as their event key, the Statsig value column as `valueColumn`, and columns in the data source's case. A data source without the constant (for example one built by hand) keeps the legacy event key, which usually matches nothing. A real run stops if the data sources cannot be read. Before `warehouse` has created them, preview with `--dry-run --assume-constant-event-key`.

Warehouse-native conversion is newer and less battle-tested than the cloud path. If a warehouse-native metric isn't recognized or converts wrong, capture its raw Statsig definition with `--dump-raw <file>` (Statsig key only) and share it — redacted — with the LaunchDarkly team; the tool sees a metric only through that JSON, so it's exactly what conversion works from.

### Custom unit types (company-level experiments)

```bash
cat > unit-types.json << 'EOF'
{"companyID": "company", "teamID": "team"}
EOF
./statsig-to-ld metrics convert --all --ld-project my-project \
  --unit-type-mapping unit-types.json
```

Without this mapping, non-`userID` unit types are lowercased and a warning is emitted.

Warehouse-native metrics often carry no `unitTypes` on the metric itself; the analysis unit lives on the metric source's id-type mapping. When such metrics are present, `metrics convert` makes one extra Console API call to list the metric sources and resolves each metric's analysis unit from its source instead of defaulting to `user`. If that call fails (or a source has no id-type mapping), the metric falls back to `user` with a warning. `--unit-type-mapping` still applies to the resolved unit names.

### Analysis units and clustered experiments

An LD metric carries `analysisUnits`: the context kinds an experiment may analyze it by, one chosen per metric when the experiment is created. That is how LaunchDarkly runs the experiment Statsig calls clustered — randomize by company, analyze per user. The list is what the converter controls; the choice itself is made later, in the experiment.

- `--widen-analysis-units` (default **off**) adds the id types a metric's source declares to that metric's list, instead of stopping at the unit types on the metric. Warehouse-native metrics take their units from the source, so that mapping is the fuller set; cloud metrics have no such source and are unaffected. It is off by default because most sources map several id types and each added unit must be registered on the LD project. For a ratio, only units both the numerator and denominator sources declare are added.
- `--extra-analysis-units user,request` adds LD context kinds directly, for units with no Statsig counterpart. Additive, not a replacement. Not subject to `--unit-type-mapping` — these are LD context kinds already.
Widening changes only what an experiment may pick, never how a metric is measured. Statsig ratio metrics whose denominator is a count-distinct convert as LD ratio metrics; nothing here rewrites them.

### Metric type conversion

| Statsig type | LD kind | Status |
|---|---|---|
| `event_count_custom` | custom | Supported |
| `sum` | custom (numeric, sum) | Supported |
| `mean` | custom (numeric, average) | Supported |
| `count_distinct` | custom (numeric, count_distinct) | Supported on a warehouse-native metric with a bound data source: the counted column goes in `unitAggregationField` and the metric is numeric (the per-unit value is a distinct count). Counting distinct **units** (no column) converts to a binary metric, which expresses the same thing. Without a data source it falls back to binary and is lossy. Ratio terms are separate and stay non-numeric. |
| `event_user` | custom (binary) | Supported. This is the participation family: an unset rollup is the daily-participation-**rate** default (lossy — see below); `max` (one-time) and `custom` (windowed) convert cleanly. |
| `event_user_window` | custom | Supported |
| `daily_participation` (unit count) | custom (binary) | Supported as a binary metric. Only the daily-participation-**rate** is lossy (LD has no fraction-of-days aggregation, so it's approximated as binary). The rate is the DEFAULT: unset `rollupTimeWindow`, `daily` (warehouse-native), or `daily_participation_rate` (cloud). Explicit `max` (one-time) and `custom` (windowed) rollups convert exactly. |
| `ratio` | custom + denominator | Supported — requires a warehouse data source (`--ld-data-source` / `--source-mapping`) |
| `funnel` | — | Not converted — would need an LD metric group |
| `composite` | — | No LD equivalent |
| `percentile` | — | LD uses percentile as analysisType, not metric type |

Windowed metrics (`event_user_window`, or a custom rollup window) and winsorization convert too — winsorization needs a numeric metric, and a custom window needs a warehouse data source (otherwise it's dropped; see below).

### Metric warnings to surface to the user

Many of these mark a conversion **lossy**: by default the metric is skipped (`skipped_lossy` in the report) with the warning as the reason, and `--convert-lossy` converts it anyway. Advisory warnings (the unit-type nudge, a truncated key) do **not** cause a skip.

| Warning | Severity | What to do |
|---|---|---|
| `DATA LOSS: ... filter criteria` | High | Lossy — skipped by default. Statsig filter criteria on a **warehouse-native** metric with a bound data source convert to an LD metric filter automatically; this warning means they could not. Common causes: no data source bound (pass `--ld-data-source` / `--source-mapping` and re-run, which usually fixes it), a cloud metric (not supported yet), or an unmappable condition (`sql_filter`, `after_exposure`, `before_exposure`). The warning lists every dropped criterion so it can be rebuilt by hand. Conversion is all-or-nothing per term: one unmappable criterion drops the whole term's filter, because a partial AND-filter would silently match MORE rows than the original. |
| `converted N ... filter criteria` | Info | Not lossy. Statsig filter criteria became an LD metric filter. Metric filters only work on Snowflake data sources today, so check the metric in LD if it uses a different warehouse. |
| `N metric events — only the first is used` | Medium | Lossy — only the first event is used; extra events are dropped. |
| `winsorization ... occurrence metric` | Low | Lossy — LD can't winsorize an occurrence metric (numeric metrics winsorize fine). |
| `per-unit capping` | Low | Lossy — per-unit cap not applied. |
| `custom rollup window` | Low | Lossy only when no data source is bound; pass `--ld-data-source` (snowflake) to apply the window. |
| `daily participation rate ... loses the per-day rate` | Medium | Lossy — the participation-rate default (unset rollup, `daily`, or `daily_participation_rate`) on `event_user`/`daily_participation` metrics, approximated as a binary metric. Explicit one-time (`max`) and windowed (`custom`) rollups convert cleanly and are not flagged. |
| `unitType ... may not match an LD context kind` | Medium | Use `--unit-type-mapping` to map explicitly. |
| `NOT CREATED: ... could not be converted to a LaunchDarkly filter` (code `constant_event_key_unfiltered`) | High | Skipped even with `--convert-lossy`, reported as `skipped_incompatible`. The metric reads a constant-key data source, where only its filter selects rows, and the filter did not convert, so it would count every row. Create it by hand in LD with the filter. |
| `metric ... already exists with event key ... so it matches no rows` (code `existing_metric_event_key_mismatch`) | High | On a `skipped_existing` entry, and listed under the run summary. The metric predates its data source's constant event key. Set its event key to the data source key in LD, or delete it and rerun. |
| `metric ... already exists and could not be read back` (code `existing_metric_unverified`) | Medium | On a `skipped_existing` entry; counted separately in the run summary, not as a mismatch. Its event key was not checked against its data source's constant. Check that the token can read metrics, or check the metric in LD. |
| `N metric(s) use columns that could not be checked against their data source` (note code `column_unverified`) | Medium | One line in the run summary; each affected metric carries the code in `note_codes`, not a warning. The data source's columns are unknown (`--assume-constant-event-key`, or it does not exist yet), so columns keep Statsig's case. Rerun with LD credentials once the data source exists. |
| `event ... is bound to data source ..., whose key column holds the constant` (code `cloud_metric_on_constant_key_source`) | Medium | A cloud metric bound to a constant-key data source (usually through `--ld-data-source`) matches no rows. Bind it to a data source with an event-name key column, or leave it unbound. |
| `N mapped data source(s) do not have the constant event key` | High | One line at the start of the run. Warehouse-native metrics on those data sources keep their Statsig event keys, which usually match none of their rows. Run `warehouse --overwrite` first to add the constant event key, then rerun `metrics convert`. |
| `... is not a column of the bound LaunchDarkly data source` (code `column_not_in_data_source`) | Medium | Lossy — skipped by default. A value, count-distinct, or filter column from Statsig is not in the data source's column list. Check the Statsig column name against the data source's columns in LD. |
| (note code `constant_event_key`) | Info | Not printed; in `note_codes` only. The term's event key is its data source's constant event key, the expected outcome on a constant-key data source. |
| `no LD data source specified` | Medium | Warehouse-native metric is being created without a data source binding. Fix: run `statsig-to-ld warehouse` first (it creates the data sources and writes `source-mapping.json`), then re-run `metrics convert --source-mapping source-mapping.json`. If the data sources already exist (set up by hand or via Terraform), pass `--ld-data-source` or `--source-mapping` directly. |

## Subcommand: warehouse

Sets up the LaunchDarkly side of a Statsig warehouse-native experimentation project: data export integration, experimentation integration, and LD metric data sources. **It does not migrate metric definitions** — `metrics convert` does that, using the `source-mapping.json` this subcommand writes. Full operator detail (interactive SQL wizards per warehouse type, resume semantics, the `migration_state.json` lifecycle) is in [`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md); this section is enough to drive the basic flow and decide when to run it.

```bash
# Dry-run from live Statsig API (only Statsig key needed). Add --ld-key and
# --ld-project to list which data sources would be created, updated, skipped, or refused.
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

# Correct existing data sources' timestamp, value, and context mappings from the export
# (preview first: add --dry-run with --ld-key and --ld-project)
./statsig-to-ld warehouse ... --only data-sources --update-mappings
```

### Phases

1. **Export** — Fetches `wh_connections` and `metric_source/list` from Statsig (or loads from `--statsig-export-file`). Writes `statsig_export_<timestamp>.json`. (Metric definitions are not fetched here — `metrics convert` re-fetches them itself.)
2. **Warehouse setup** (interactive) — Checks for existing data-export and experimentation integrations in LD; if absent, runs the wizard. Snowflake / BigQuery / Databricks / Redshift each have their own setup path. Auto-skips if integrations already exist.
3. **Data sources** — Creates LD data sources (calling the warehouse preview API to discover real column schemas first), each wrapped to project a constant `LD_EVENT_KEY` column holding the data source key, which becomes its event key column (`--constant-event-key=false` turns this off). A data source gets a value column only when its Statsig source defines one and its query returns it, never the preview's guess (the run prints one line naming those whose query does not); numeric metrics take theirs from the metric, which `metrics convert` sets only on constant-key data sources. Each id-type mapping becomes a context kind named as `metrics convert` names analysis units without `--unit-type-mapping` (`userID` is `user`, any other unit ID is lowercased); a source whose unit IDs map to one kind with different columns fails. An existing data source that already has that column never has its query rewritten, nor does one whose wrapper was edited in LD (skipped as "kept: edited in LaunchDarkly"); `--update-mappings` can still change their mappings. One without it is skipped, and the run prints one line counting them; `--overwrite` updates its query, key column, and column list in place, keeping its timestamp, value, and context mappings while the new query returns them, but refuses (fails the source) when metrics bound to it use other event keys, since they would match no rows afterwards. `--force-overwrite` updates it anyway and lists them. It also refuses, even with `--force-overwrite`, when the new query drops a value column that bound numeric metrics without one of their own read, or the column of a context kind bound metrics use as an analysis or randomization unit. With `--constant-event-key=false`, `--overwrite` keeps the existing key column instead and fails the source if the new query does not return it. Each update is a JSON Patch that first tests the query and column mappings the run listed, so a data source changed in LD during the run fails rather than being overwritten. `--update-mappings` corrects an existing data source's timestamp, value, and context mappings in place from the Statsig export, with or without the constant event key, and leaves its query, key column, and columns alone; it adds or moves context kinds but never removes one, skips an export column of the wrong type, keeps a value column that bound numeric metrics without their own value column read, and with `--overwrite` makes an updated query take its mappings from the export ([Correcting mappings](docs/cli-reference.md#correcting-mappings)). Bound metrics are those whose numerator or denominator is on the data source, a denominator with no data source of its own reading the numerator's. A key already used by a data source in another LD environment fails that source. Statsig sources whose names map to the same key all fail and are left out of `source-mapping.json`, as are sources whose key is used in another environment. It then writes `source-mapping.json` mapping each Statsig metric source name to the LD data source key it created. The subcommand prints the recommended `metrics convert --source-mapping source-mapping.json` hand-off command at the end of a successful run.

### Report notes

`migration_report_<timestamp>.json` keeps per-data-source outcomes as `notes` (`{code, data_source}`, plus a `changes` list on `mappings_updated`); the run prints at most one line per code, not one per data source:

| Note code | Meaning |
|---|---|
| `exists_without_constant_key` | The data source exists without the constant event key and `--overwrite` was not set, so its query was left as is (`--update-mappings` may still have changed its mappings). The run prints one line counting these. |
| `constant_key_edited_in_launchdarkly` | The data source opens with the constant event-key wrapper but was edited in LD after creation, so its query was kept as is. |
| `constant_key_kept` | `--constant-event-key=false` did not remove the constant from a data source that has it. |
| `statsig_value_column_not_in_query` | The Statsig source defines a value column its query does not return, so the data source did not get it. The run prints one line naming these. |
| `mappings_updated` | `--update-mappings` changed the data source's mappings. `changes` lists each changed field as `field: old → new` (for example `["timestamp: TS → CREATED_AT", "context user: USER_ID → UID"]`), the form `--overwrite`'s warnings use. Not printed. |
| `context_kind_not_in_export` | `--update-mappings` kept a context kind the Statsig export does not map, such as one renamed by hand in LD; kinds are never removed. Not printed. |
| `value_column_kept` | `--update-mappings` kept a value column the export does not map, because bound numeric metrics without their own value column read it. The run prints one line naming these and the metrics. |
| `export_column_not_in_query` | `--update-mappings` left a mapping as it was, because the data source's columns do not include the export's column. The run prints one line naming these. |
| `export_column_wrong_type` | `--update-mappings`, or an `--overwrite` fallback, did not use an export column whose type does not fit: a timestamp column needs a timestamp or date type, a value column a numeric one. The run prints one line naming these. |

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
| `warehouse` | `statsig_export_<timestamp>.json` (Phase 1 export) + `migration_state.json` (Phase 2/3 progress, for `--resume`) + `migration_report_<timestamp>.json` (counts, warnings, errors, and [notes](#report-notes)) + `source-mapping.json`. A `--dry-run` also writes `data-source-bodies.json`: the data source bodies a real run would send, with the wrapped SQL, before the preview (an `error` field marks a source that cannot be created). |

Useful `jq` queries:

```bash
# metrics convert: summary counts
cat migration-report.json | jq '{total: .statsig_metrics_total, dry_run, converted, with_warnings: .converted_with_warnings, skipped_existing, skipped_incompatible, skipped_lossy, failed}'

# metrics convert: per-type breakdown (which metric types convert vs drive the incompatible/failed buckets)
cat migration-report.json | jq '.by_type'
# just the types with failures or incompatibilities, worst first
cat migration-report.json | jq '.by_type | to_entries | map(select(.value.failed + .value.skipped_incompatible > 0)) | sort_by(-(.value.failed + .value.skipped_incompatible)) | from_entries'

# metrics convert: DATA LOSS warnings (most critical)
cat migration-report.json | jq '.metrics[] | select(.warnings[]? | contains("DATA LOSS")) | {name: .statsig_name, warnings}'

# --- diagnostics: aggregate on codes, not on message text (wording changes, codes do not) ---

# every warning code by frequency — the fastest read on what a run actually hit
cat migration-report.json | jq -r '.metrics[].warning_codes[]?' | sort | uniq -c | sort -rn
# just the codes that caused a skip
cat migration-report.json | jq -r '.metrics[].lossy_codes[]?' | sort | uniq -c | sort -rn
# metrics never created whatever --convert-lossy says, and why
cat migration-report.json | jq '.metrics[] | select(.blocking_codes != null) | {name: .statsig_name, reason}'
# existing metrics whose event key their constant-key data source never holds
cat migration-report.json | jq -r '.metrics[] | select(.warning_codes[]? == "existing_metric_event_key_mismatch") | .ld_key'
# note codes (recorded, never printed per metric) by frequency
cat migration-report.json | jq -r '.metrics[].note_codes[]?' | sort | uniq -c | sort -rn
# warehouse: data source outcomes kept as notes (codes in the table below)
cat migration_report_*.json | jq -r '.notes[]? | "\(.code)\t\(.data_source)"' | sort
# warehouse --update-mappings: what changed on each data source
cat migration_report_*.json | jq -r '.notes[]? | select(.code == "mappings_updated") | "\(.data_source)\t\(.changes | join("; "))"'

# metric filters: how many terms converted vs were blocked
cat migration-report.json | jq '[.metrics[].filters[]?] | group_by(.applied) | map({applied: .[0].applied, terms: length, criteria: (map(.criteria) | add)})'
# why filters were blocked, and which Statsig condition is responsible
cat migration-report.json | jq -r '.metrics[].filters[]? | select(.applied == false) | "\(.blocked_by)\t\(.blocked_condition // "-")"' | sort | uniq -c | sort -rn
# the metrics a --source-mapping re-run would fix (blocked only for lack of a data source)
cat migration-report.json | jq '[.metrics[] | select(any(.filters[]?; .blocked_by == "no_data_source")) | .statsig_name] | {count: length, names: .[0:10]}'

# data source binding, which gates filters and window offsets
cat migration-report.json | jq '[.metrics[] | {bound: (.ld_data_source != null)}] | group_by(.bound) | map({bound: .[0].bound, count: length})'
# Statsig sources that resolved to nothing — these are the --source-mapping entries to add
cat migration-report.json | jq -r '.metrics[] | select(.ld_data_source == null and .statsig_source_name != null) | .statsig_source_name' | sort | uniq -c | sort -rn

# analysis unit distribution (which metrics are analyzed at a non-user grain)
cat migration-report.json | jq -r '.metrics[].analysis_units[]?' | sort | uniq -c | sort -rn

# daily participation: split the lossy rate from the clean one-time/windowed rollups
cat migration-report.json | jq -r '.metrics[] | select(.statsig_type == "daily_participation") | .statsig_rollup_time_window // "(unset = rate)"' | sort | uniq -c | sort -rn

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
Already created in a previous run — expected and safe. The tool is idempotent. Existing metrics are not updated, so one created before its data source got the constant event key is flagged with `existing_metric_event_key_mismatch`; fix its event key in LD.

### `warehouse` warns "N data source(s) already exist without the constant event key"
Those data sources were created before the constant event key, or by hand; each is listed in the progress output as "exists without the constant event key". If this tool created them, rerun `warehouse --dry-run` with LD credentials and `--overwrite` to see what would happen, then rerun with `--overwrite` to update them in place (LD cannot delete metric data sources). If one was built by hand and its key column holds real event names, leave it.

### `warehouse --overwrite` fails a data source with "bound to it use an event key other than"
Those metrics would match no rows once the key column holds only the data source key. Metrics created by older versions of this tool usually match nothing already. Rerun with `--force-overwrite` to update it anyway, then set each listed metric's event key to the data source key in LD.

### `warehouse --overwrite` fails a data source with "changed in LaunchDarkly after this run read it" or "could not read a complete list of the project's metrics"
Nothing was changed. The first means someone edited the data source during the run; the second means LD's metric list came back short twice, so the bound metrics could not be checked. Rerun.

### `warehouse` fails a data source with "is taken by a data source in LaunchDarkly environment"
Data source keys are unique per project, and that key belongs to a data source in another environment, which this run will not change or map. Rename the source in Statsig, or run against that environment with `--ld-environment`.

### `warehouse --overwrite` fails a data source with "its value column ... is not in the updated query" or "does not return the column of its context kind(s)"
The update would remove a mapping bound metrics use, and LD does not check that on the PATCH. Keep the column in the Statsig SQL, or set the listed metrics' own value column (or change their units) in LD and refresh them on running experiments, then rerun. Running experiment iterations keep the metric version they started with, which the check cannot see. With `--update-mappings` too, the check runs on the patch that would be sent.

### `warehouse` fails a data source with "all map to context kind"
Two of the source's Statsig unit IDs (for example `userID` and `user`) map to one LD context kind with different columns. Keep one of them in the Statsig source's id-type mapping and rerun, or set the data source's contexts by hand.

### `warehouse --update-mappings` warns "kept a value column", "their columns do not include the export's", or "the export's column has the wrong type"
One line each per run; the rest of each data source's mappings were applied, and each listed data source carries the note code in the report. For the first, bound numeric metrics read the data source's value column; set their own value column in LD and refresh them on running experiments, then rerun to remove it. For the second, correct the column in Statsig, or add it to the data source's query in LD. For the third, a timestamp column needs a timestamp or date type and a value column a numeric one; correct the column in Statsig, or cast it in the query.

### `warehouse --update-mappings` fails a data source with "the warehouse now returns different columns"
LD reruns the query to validate a mapping change and compares the columns exactly. Run the query preview in the data source's LD editor and save it to refresh its columns, then rerun.

### `warehouse --constant-event-key=false --overwrite` fails a data source with "its key column ... is not in the updated query"
In that mode an update keeps the existing key column, and the new Statsig SQL no longer returns it, so metrics on the data source would filter a different column. Keep the column in the Statsig SQL, or update the data source by hand.

### `metrics convert` stops with "listing metric data sources"
A real run will not convert warehouse-native metrics without reading their data sources, since the event keys come from them. Check that the token can read the project's metric data sources, or use `--dry-run` to preview.

### `targeting import` skips a flag with `skipped_lossy`
The Statsig source uses a lossy feature (segments, gate prerequisites, custom unit_id, multi-variant overrides, unreachable rules). Either accept the loss with `--accept-data-loss=...` or address the lossy condition first (recreate the segment in LD, set up an LD flag prerequisite, etc.).

### "key collision" warnings and failures
Two Statsig entities with different names (e.g. `revenue (gross)` and `revenue/gross`) sanitize to the same LD key. `flags import` and `metrics convert` create only the first and skip the other. `warehouse` fails every metric source in the group instead: none of them is created, updated, or written to `source-mapping.json`. Rename all but one in Statsig and rerun (for `warehouse`, or create those data sources by hand with distinct keys and bind them with `metrics convert --source-mapping`).

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
7. **Clustered experiments?** If the user randomizes by one unit and measures by another, confirm which LD context kinds each metric should be analyzable by — `--widen-analysis-units` and `--extra-analysis-units` above.
8. **Numeric metric units?** If known, use `--default-unit` to set a meaningful label (default is `"units"`).
9. **EU/FedRAMP?** If yes, need `--ld-url https://app.eu.launchdarkly.com`.
10. **SDK call-site rewrites?** Not handled by this CLI. If the user is migrating application code (Statsig SDK → LD SDK) for JS / TS / React / Node, point them at the [SDK-rewrite skill](skills/statsig-to-launchdarkly-migrator/SKILL.md). For other languages, point them at [`docs/migration-playbook.md`](docs/migration-playbook.md) §1.

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
