# Statsig Metric Importer CLI

Converts Statsig metric definitions into LaunchDarkly metrics. Supports both Statsig Cloud and Warehouse Native metrics, with idempotent re-runs, parallel processing, and structured migration reports.

## Prerequisites

- Go 1.24+ (to build from source)
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API Access Token** (`api-xxx`) — create at Account Settings > Authorization

## Installation

### From source

```bash
go build -o statsig-metric-importer .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-metric-importer-cli/cmd.version=1.0.0" \
  -o statsig-metric-importer .
```

### Pre-built binaries

Download from the [Releases](https://github.com/launchdarkly-labs/statsig-metric-importer-cli/releases) page.

## API Key Security

API keys can be provided in three ways (in order of precedence):

1. **Command-line flags** (`--statsig-key`, `--ld-key`) — visible in shell history and `ps` output. Use only in CI/CD where keys are injected from a secrets manager.
2. **Environment variables** (`STATSIG_CONSOLE_KEY`, `LD_API_KEY`) — not visible in `ps`, but `export KEY=value` is logged to shell history and the variable persists for the shell session (visible to child processes). Use the `read` approach below to avoid history exposure.
3. **Interactive prompt** — **most secure for interactive use**. If no key is provided via flag or env var, the tool prompts with echo disabled. The key never touches disk, shell history, environment, or process listings.

**Recommended: let the tool prompt you.** Just run the command without any key flags — you'll be prompted to enter each key with input hidden:

```bash
./statsig-metric-importer convert --all --dry-run
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
export STATSIG_CONSOLE_KEY=console-YOUR_KEY
export LD_API_KEY=api-YOUR_KEY

# 2. Preview what will happen (no metrics created)
./statsig-metric-importer convert --all --dry-run

# 3. Review the migration report
cat migration-report.json | jq '.metrics[] | select(.warnings | length > 0)'

# 4. Run the actual migration
./statsig-metric-importer convert --all --ld-project my-project
```

## Usage

### Preview all metrics (dry run)

```bash
./statsig-metric-importer convert --all --dry-run
```

### Convert a single metric

```bash
./statsig-metric-importer convert --metric purchase_revenue \
  --ld-project my-project
```

### Bulk convert all metrics

```bash
./statsig-metric-importer convert --all --ld-project my-project
```

### Filter by type or tag

```bash
# Only convert sum and mean metrics
./statsig-metric-importer convert --all --include-types sum,mean --ld-project my-project

# Only convert metrics tagged "p0"
./statsig-metric-importer convert --all --include-tags p0 --ld-project my-project
```

### Set a default unit for numeric metrics

```bash
./statsig-metric-importer convert --all --default-unit "$" --ld-project my-project
```

Without `--default-unit`, numeric metrics get `unit: "units"` (a generic placeholder). Pass `--default-unit` to set a meaningful label like `$`, `ms`, or `count` at conversion time, or update the unit in the LD UI later.

### CSV output

```bash
./statsig-metric-importer convert --all --format csv --ld-project my-project
```

### With Warehouse Native data source

```bash
# Single data source for all WH Native metrics
./statsig-metric-importer convert --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Multiple data sources via mapping file
./statsig-metric-importer convert --all --ld-project my-project \
  --source-mapping sources.json
```

Where `sources.json` maps Statsig source names to LD data source keys:

```json
{
  "purchases_table": "snowflake-purchases-ds",
  "sessions_table": "snowflake-sessions-ds"
}
```

### Custom unit types (company-level experiments)

If your Statsig project uses unit types beyond `userID` (e.g. `companyID`, `teamID`), map them to your LD context kinds:

```bash
./statsig-metric-importer convert --all --ld-project my-project \
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

### EU / FedRAMP instances

```bash
./statsig-metric-importer convert --all --ld-project my-project \
  --ld-url https://app.eu.launchdarkly.com
```

## Flags

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

Releases are driven by Git tags. Pushing a `v*` tag triggers the CI workflow to cross-compile binaries for macOS, Linux, and Windows, then publish them to the [Releases page](https://github.com/launchdarkly-labs/statsig-metric-importer-cli/releases) automatically. Release notes are generated from commits since the previous tag.

```bash
git tag v1.2.3
git push origin v1.2.3
```
