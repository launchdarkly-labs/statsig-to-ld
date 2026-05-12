# statsig-to-ld

Migrate from Statsig to LaunchDarkly. Single CLI covering two paths:

- **`metric-import`** — Statsig metric definitions → LaunchDarkly metrics (Statsig Cloud + Warehouse Native)
- **`flag-import`** — Statsig feature gates / dynamic configs → LaunchDarkly flags, with per-environment targeting rules, percentage rollouts, and user overrides

Both subcommands share the same credentials, the same idempotent re-run contract, and the same migration report format.

## Prerequisites

- Go 1.24+ (to build from source)
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API access token** (`api-xxx`) — create at **Account settings → Authorization → Access tokens**. Required permissions:
  - For `metric-import`: Writer (or any role with `metric:create`)
  - For `flag-import`: Writer (or any role with `flag:create` + `flag:update` + `environment:create`). `environment:create` is only needed if Statsig has environments that don't yet exist in LD.
  - Not an SDK key or client-side ID. Use a service token for shared automation.

## Installation

### From source

```bash
go build -o statsig-to-ld .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-to-ld-cli/cmd.version=1.0.0" \
  -o statsig-to-ld .
```

### Pre-built binaries

Download from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld-cli/releases) page.

## API Key Security

API keys can be provided in three ways (in order of precedence):

1. **Command-line flags** (`--statsig-key`, `--ld-key`) — visible in shell history and `ps` output. Use only in CI/CD where keys are injected from a secrets manager.
2. **Environment variables** (`STATSIG_CONSOLE_KEY`, `LD_API_KEY`) — not visible in `ps`, but `export KEY=value` is logged to shell history and the variable persists for the shell session (visible to child processes). Use the `read` approach below to avoid history exposure.
3. **Interactive prompt** — **most secure for interactive use**. If no key is provided via flag or env var, the tool prompts with echo disabled. The key never touches disk, shell history, environment, or process listings.

**Recommended: let the tool prompt you.** Just run the command without any key flags — you'll be prompted to enter each key with input hidden:

```bash
./statsig-to-ld metric-import --all --dry-run
# Enter Statsig Console API key (console-xxx): <hidden input>
```

**If you prefer env vars** (e.g., running multiple commands in a session), avoid `export KEY=value` in your shell directly — it lands in history. Instead:

```bash
# Set without history exposure (prompts for value with hidden input)
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY
```

**For CI/CD pipelines**, flags or env vars injected from a secrets manager (e.g., GitHub Actions secrets, Vault) are appropriate since there is no interactive terminal.

## Quick Start

```bash
# 1. Set API keys (recommended — avoids shell history exposure)
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY

# 2. Migrate metrics
./statsig-to-ld metric-import --all --dry-run                          # preview
./statsig-to-ld metric-import --all --ld-project my-project            # run

# 3. Migrate feature gates (one direction; --kind=dynamic-configs for DCs)
./statsig-to-ld flag-import --kind feature-gates --dry-run             # preview
./statsig-to-ld flag-import --kind feature-gates --ld-project my-project # run
```

The keys (`STATSIG_CONSOLE_KEY`, `LD_API_KEY`) are shared across both subcommands — export once, use for both.

## Usage

> Two subcommands: **`metric-import`** handles Statsig metrics; **`flag-import`** handles Statsig feature gates and dynamic configs. The sections below cover each in turn.

### metric-import

#### Preview all metrics (dry run)

```bash
./statsig-to-ld metric-import --all --dry-run
```

#### Convert a single metric

```bash
./statsig-to-ld metric-import --metric purchase_revenue \
  --ld-project my-project
```

#### Bulk convert all metrics

```bash
./statsig-to-ld metric-import --all --ld-project my-project
```

#### Filter by type or tag

```bash
# Only convert sum and mean metrics
./statsig-to-ld metric-import --all --include-types sum,mean --ld-project my-project

# Only convert metrics tagged "p0"
./statsig-to-ld metric-import --all --include-tags p0 --ld-project my-project
```

#### Set a default unit for numeric metrics

```bash
./statsig-to-ld metric-import --all --default-unit "$" --ld-project my-project
```

Without `--default-unit`, numeric metrics get `unit: "units"` (a generic placeholder). Pass `--default-unit` to set a meaningful label like `$`, `ms`, or `count` at conversion time, or update the unit in the LD UI later.

#### CSV output

```bash
./statsig-to-ld metric-import --all --format csv --ld-project my-project
```

#### With Warehouse Native data source

```bash
# Single data source for all WH Native metrics
./statsig-to-ld metric-import --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Multiple data sources via mapping file
./statsig-to-ld metric-import --all --ld-project my-project \
  --source-mapping sources.json
```

Where `sources.json` maps Statsig source names to LD data source keys:

```json
{
  "purchases_table": "snowflake-purchases-ds",
  "sessions_table": "snowflake-sessions-ds"
}
```

#### Custom unit types (company-level experiments)

If your Statsig project uses unit types beyond `userID` (e.g. `companyID`, `teamID`), map them to your LD context kinds:

```bash
./statsig-to-ld metric-import --all --ld-project my-project \
  --unit-type-mapping unit-types.json
```

Where `unit-types.json` maps Statsig unit types to LD context kind names:

```json
{
  "companyID": "company",
  "teamID": "team"
}
```

Without this mapping, non-`userID` unit types are lowercased (e.g. `companyID` → `companyid`) and a warning is emitted. Ensure matching context kinds exist in your LD project before running experiments with the migrated metrics.

#### EU / FedRAMP instances

```bash
./statsig-to-ld metric-import --all --ld-project my-project \
  --ld-url https://app.eu.launchdarkly.com
```

### flag-import

Imports Statsig **feature gates** as LD boolean flags, or **dynamic configs** as LD JSON multi-variate flags. Per-environment targeting rules + percentage rollouts + user overrides are translated and applied via JSON Patch after flag creation.

Pick one entity type per run via `--kind`:

```bash
# Preview gate import (no LD writes)
./statsig-to-ld flag-import --kind feature-gates --dry-run

# Import all feature gates with targeting
./statsig-to-ld flag-import --kind feature-gates --ld-project my-project

# Import dynamic configs as JSON multi-variate flags
./statsig-to-ld flag-import --kind dynamic-configs --ld-project my-project

# Apply a tag to all imported flags (and to auto-created LD environments)
./statsig-to-ld flag-import --kind feature-gates --ld-project my-project \
  --tag imported-from-statsig

# Filter by a Statsig tag — only import gates tagged "team-payments"
./statsig-to-ld flag-import --kind feature-gates --ld-project my-project \
  --include-tag team-payments

# Create flag shells only, skip per-environment targeting
./statsig-to-ld flag-import --kind feature-gates --ld-project my-project \
  --no-targeting
```

**Targeting translation:** Statsig rule conditions are mapped to LD clauses through a fixed operator + condition table. Approximations (e.g. `version_gte` → `semVerGreaterThan` because LD has no `OrEqual` variant) are noted in the report. Unmappable conditions (`passes_segment`, `fails_gate`, etc.) cause the entire rule to be dropped with a warning — half-translated rules would silently evaluate wrong.

**Environment reconciliation:** Statsig env names are mapped to LD env keys case-insensitively. Missing LD envs are auto-created with the optional `--tag` applied. 403 (no `environment:create` permission) marks the env unreachable but doesn't abort — rules scoped to that env are skipped with a note.

**Idempotency:** Existing LD flags (matched by key) are detected via a pre-flight list and skipped. Re-running after a partial failure resumes from where it left off.

## metric-import flags

| Flag | Default | Description |
|---|---|---|
| `--metric` | — | Convert a single Statsig metric by name |
| `--all` | `false` | Convert all Statsig metrics |
| `--dry-run` | `false` | Preview conversion without creating LD metrics |
| `--statsig-key` | — | Statsig Console API key (or `STATSIG_CONSOLE_KEY` env) |
| `--statsig-url` | Statsig Cloud | Statsig API base URL override |
| `--ld-key` | — | LaunchDarkly API access token (or `LD_API_KEY` env) |
| `--ld-url` | US Cloud | LaunchDarkly API base URL (for EU/FedRAMP) |
| `--ld-project` | — | LaunchDarkly project key (required) |
| `--ld-data-source` | — | LD data source key for all Warehouse Native metrics |
| `--source-mapping` | — | JSON file mapping Statsig source names to LD data source keys |
| `--unit-type-mapping` | — | JSON file mapping Statsig unit types to LD context kinds |
| `--output` | `migration-report.json` | Path for the migration report |
| `--format` | `json` | Report format: `json` or `csv` |
| `--default-unit` | — | Unit of measure for numeric metrics (e.g. `$`, `ms`, `count`) |
| `--include-tags` | — | Only convert metrics with these Statsig tags (comma-separated) |
| `--include-types` | — | Only convert metrics of these Statsig types (comma-separated) |
| `--concurrency` | `10` | Max concurrent LD API requests for bulk conversion |

## flag-import flags

| Flag | Default | Description |
|---|---|---|
| `--kind` | — | `feature-gates` or `dynamic-configs` (required) |
| `--statsig-key` | — | Statsig Console API key (or `STATSIG_CONSOLE_KEY` env) |
| `--ld-key` | — | LaunchDarkly API access token (or `LD_API_KEY` env) |
| `--ld-project` | — | LaunchDarkly project key (required for non-dry-run) |
| `--include-tag` | — | Only import Statsig gates/configs with this tag |
| `--tag` | — | Tag to apply to all imported LD flags + auto-created environments |
| `--maintainer-id` | — | LD member ID to set as the flag maintainer |
| `--no-targeting` | `false` | Skip per-env targeting (create flag shells only) |
| `--dry-run` | `false` | Preview the import without writing to LaunchDarkly |
| `--override-workers` | `10` | Concurrent workers fetching Statsig overrides |
| `--output` | `flag-import-report.json` | Path for migration report output |
| `--format` | `json` | Report format: `json` or `csv` |

## Type Conversion Mapping

| Statsig Type | LD kind | isNumeric | unitAggregationType | Status |
|---|---|---|---|---|
| `event_count_custom` | custom | false | sum | Supported |
| `sum` | custom | true | sum | Supported |
| `mean` | custom | true | average | Supported |
| `event_user` | custom | false | average | Supported |
| `event_user_window` | custom | false | average | Supported |
| `ratio` | — | — | — | Not yet supported in LD |
| `funnel` | — | — | — | Requires LD metric group |
| `composite` | — | — | — | Not supported in LD |
| `percentile` | — | — | — | Not supported as LD type |

## Statsig Features Not Carried Over

These Statsig-specific features are detected and logged as warnings in the migration report. Metrics with these features are still converted, but the feature is not applied in LD:

| Feature | Warning | Impact |
|---|---|---|
| **Event filter criteria** | `DATA LOSS` | LD metric matches all events, not just filtered subset. Manual filter setup required. |
| **Winsorization** | Outlier clipping not applied | Experiment results may be more sensitive to outliers |
| **Per-unit capping** | Daily cap not applied | No per-user-per-day value cap |
| **Log transform** | Values not log-transformed | Distribution shape may differ |
| **Custom rollup windows** | Measurement windows not applied | LD uses full experiment duration |
| **Daily participation rate** | Uses standard binary conversion | Different aggregation method |
| **Count distinct** | Counts all occurrences instead | Higher counts than Statsig |
| **Metadata aggregation** | Aggregates `track()` metricValue | Ensure events send correct value |

## Migration Report

Each run writes a report (JSON or CSV) and prints a summary table:

```
Migration Summary
─────────────────────────────────────
  Total Statsig metrics:  150
  Converted:              95
    with warnings:        23
  Already existing (skipped):  10
  Incompatible (skipped): 45
  Failed:                 0
─────────────────────────────────────
Report written to migration-report.json
```

### JSON format

```json
{
  "timestamp": "2026-05-15T10:30:00Z",
  "statsig_metrics_total": 150,
  "converted": 95,
  "converted_with_warnings": 23,
  "skipped_existing": 10,
  "skipped_incompatible": 45,
  "failed": 0,
  "metrics": [...]
}
```

### CSV format

```csv
statsig_name,statsig_type,statsig_id,status,ld_key,ld_project,warnings,reason
purchase_revenue,sum,purchase_revenue::sum,converted,purchase-revenue-sum,my-project,,
conversion_rate,ratio,conversion_rate::ratio,skipped_incompatible,,,,Not yet supported
```

## Idempotency

The LD metric key is derived from the Statsig metric ID (`name::type`), e.g., `purchase_revenue::sum` becomes `purchase-revenue-sum`. Re-running the tool for the same metrics is safe — existing LD metrics are detected (HTTP 409) and recorded as `skipped_existing` in the report.

## Incremental Migration

The tool supports incremental migration strategies:

1. **By type**: Start with simple types first: `--include-types event_count_custom,sum`
2. **By tag**: Migrate tagged subsets: `--include-tags p0,critical`
3. **Re-run safely**: As LD adds support for new metric types (ratio, funnel), re-run `--all` — previously converted metrics are skipped, newly compatible metrics are created.

## Releasing a New Version (Contributors)

Releases are driven by Git tags. Pushing a `v*` tag triggers the CI workflow to cross-compile binaries for macOS, Linux, and Windows, then publish them to the [Releases page](https://github.com/launchdarkly-labs/statsig-to-ld-cli/releases) automatically. Release notes are generated from commits since the previous tag.

```bash
git tag v1.2.3
git push origin v1.2.3
```
