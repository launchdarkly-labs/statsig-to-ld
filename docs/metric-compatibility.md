# Statsig → LaunchDarkly metric compatibility

What `statsig-to-ld metrics convert` carries over from Statsig to LaunchDarkly,
and how thoroughly each path has been verified. Use it to set expectations before
a migration and to see which conversions are proven vs. still provisional.

## Status (as of 2026-07-17)

`main` now has the base metric types, cloud ratio, windowed + winsorized metrics,
the incompatible-type skips, and the full warehouse-native path. Since the last
update the following landed:

- **Skip-by-default for lossy conversions.** A conversion that would drop or
  approximate a Statsig feature is now skipped and reported as `skipped_lossy`
  instead of being silently created as a degraded LaunchDarkly metric.
  `--convert-lossy` opts back in. Every ⚠️ Lossy row below reflects this.
- **Warehouse-native conversion, corrected to the real Statsig shape** and
  live-validated. Warehouse-native `sum` and `mean` metrics were created in a
  LaunchDarkly staging project bound to a Snowflake data source. Warehouse-native
  is no longer experimental; the remaining gaps are called out per row.
- **event_count via lineage.** Built-in `event_count` metrics carry their event in
  `lineage.events` (not `metricEvents`), so they were failing; they now convert.
- **Analysis unit from the metric source.** Warehouse-native metrics usually have
  no `unitTypes` of their own, so the unit is resolved from the source's
  `idTypeMapping` instead of defaulting to `user`.
- **Data-source requirement surfaced.** Warehouse-native and ratio metrics need a
  LaunchDarkly data source; the run flags any that resolve none (ratio is rejected
  without one, others are created unbound).
- **Daily participation split by rollup mode.** Only the participation rate is
  lossy now; the one-time and windowed unit-count rollups convert cleanly.

## Verification tiers

| Tier | Meaning |
|---|---|
| ✅ Verified E2E | A real Statsig metric of this kind was converted by the tool and created in a LaunchDarkly staging project; the result was fetched back and field-checked. |
| 🟢 Unit-tested | Conversion logic is covered by unit tests; the LD shape was not independently created via the tool (noted where it was exercised indirectly). |
| 🟡 Provisional | Logic is in place and unit-tested, but the exact Statsig shape or the full path has not been confirmed against a live response of this specific kind. |
| ⛔ Incompatible | No LaunchDarkly equivalent; the metric is skipped and reported (`skipped_incompatible`). |
| ⚠️ Lossy | A Statsig feature can't be represented in LaunchDarkly. The metric is skipped by default (`skipped_lossy`, with the dropped feature as the reason); `--convert-lossy` converts it anyway, dropping that feature and emitting a warning. |

## Metric types

| Statsig type | LaunchDarkly result | Status |
|---|---|---|
| `event_count_custom` / `event_count` / `count` | custom, non-numeric, unit aggregation `sum`. Built-in `event_count` reads its event from `lineage.events`. | 🟢 Unit-tested (lineage path dry-run verified) |
| `sum` | custom, numeric, `sum`, `eventDefault {disabled:false, value:0}` | ✅ Verified E2E (warehouse-native) |
| `mean` | custom, numeric, `average`, `eventDefault {disabled:true}` | ✅ Verified E2E (warehouse-native) |
| Unit Count family: `event_user` (cloud) or `daily_participation` (warehouse-native) | non-numeric, `average` (a per-unit binary). The **rollup mode** decides fidelity: one-time (`rollupTimeWindow: max`) and windowed (`custom`) convert cleanly; the daily-participation **rate** (unset rollup, `daily`, or `daily_participation_rate`) is a per-unit fraction of active days with no LD equivalent, so it's approximated as binary. | 🟢 Clean rollups unit-tested; ⚠️ Lossy for the rate. Rollup values live-confirmed. |
| `event_user_window` | non-numeric `average` with the window applied (see Windowed, below) | 🟢 Unit-tested (windowed path ✅ E2E) |
| `count_distinct` (non-ratio) | LaunchDarkly allows `count_distinct` only on ratio metrics (confirmed: HTTP 400 otherwise), so a non-ratio one maps to a binary metric (non-numeric `average`). Faithful when it counts distinct units; loses the count when it counts distinct values of a column. | ✅ Created E2E as binary; ⚠️ Lossy when counting a column's distinct values |
| `ratio` (cloud / event-based) | LD ratio: numerator = `metricEvents[1]`, denominator = `metricEvents[0]` (Statsig stores the pair positionally). Requires a warehouse data source or LD rejects creation. | ✅ Created E2E; direction unit-tested |
| `ratio` (warehouse-native) | numerator/denominator come from `warehouseNative` (`valueColumn`, `denominatorValueColumn`) with per-term data sources | 🟡 Provisional (shape follows Statsig's Terraform contract; not yet confirmed against a live warehouse-native ratio) |
| `funnel` | no equivalent (would need an LD metric group) | ⛔ Incompatible |
| `composite` / `composite_sum` | no equivalent | ⛔ Incompatible |
| `percentile` | no equivalent (LD uses percentile as an analysis type, not a metric type) | ⛔ Incompatible |
| `user` | no equivalent | ⛔ Incompatible |
| `undefined` (setup incomplete) | no equivalent; finish configuring the metric in Statsig first | ⛔ Incompatible |

## Features & modifiers (orthogonal to type)

| Feature | Statsig source | LaunchDarkly mapping | Status |
|---|---|---|---|
| Directionality | `increase` / `decrease` | `HigherThanBaseline` / `LowerThanBaseline` | ✅ |
| Analysis (randomization) unit | `unitTypes`; for warehouse-native metrics with none, the source's `idTypeMapping` | `randomizationUnits` (`userID` → `user`); override via `--unit-type-mapping` | ✅ (source lookup added; `idTypeMapping` parsing verified against Statsig's contract) |
| Data-source binding | warehouse-native metric source name | `--ld-data-source <key>` or `--source-mapping <file>`; a run reports metrics that resolve none | ✅ (ratio is rejected without one; others create unbound) |
| Windowed (custom rollup) | `rollupTimeWindow=custom`, `customRollUpStart/End` (days); read from `warehouseNative` for WHN metrics | `windowStartOffset` / `windowEndOffset` (ms) when a data source is bound | ✅ Verified E2E (bound); ⚠️ Lossy when no data source (LD windows are snowflake-experimentation only) |
| Winsorization | `warehouseNative.winsorizationLow/High` (fractions, 0–1) | `winsorLowerPercentile` / `winsorUpperPercentile` (0–100) on numeric metrics | 🟢 Unit-tested; ⚠️ Lossy on occurrence metrics (LD can't winsorize those) |
| Daily participation rate | Unit Count family with the rate rollup (unset, `daily`, or `daily_participation_rate`) | no faithful LD equivalent; a binary metric loses the per-day fraction | ⚠️ Lossy |
| Warehouse-native filters | `warehouseNative.criteria` (numerator and denominator) | data loss: filters not applied, the LD metric matches all rows | ⚠️ Lossy |
| Event filters / criteria | `metricEvents[].criteria` | data loss: filters not applied, the LD metric matches all events | ⚠️ Lossy |
| Multiple metric events | more than one entry in `metricEvents` | only the first event is used; the rest are dropped | ⚠️ Lossy |
| Metadata aggregation | `metricEvents[].type=metadata` | not carried over (LD aggregates the tracked value) | ⚠️ Lossy |
| Per-unit capping | `warehouseNative.cap` | unsupported in LD | ⚠️ Lossy |
| Log transform | `warehouseNative.useLogTransform` | unsupported in LD | ⚠️ Lossy |
| Value threshold | `warehouseNative.valueThreshold` | unsupported in LD | ⚠️ Lossy |
| CUPED / dimension columns / wait-for-cohort / bake days | `warehouseNative` advanced analysis fields | not carried over; the core metric definition is unaffected | Advisory (not lossy) |

**About the ⚠️ Lossy rows:** by default these metrics are skipped and listed in
`migration-report.json` as `skipped_lossy`, with the dropped feature as the reason.
Re-run with `--convert-lossy` to convert them anyway; the conversion then goes
through with the feature dropped and the same reason surfaced as a warning.
Advisory notes (a non-standard unit type, a truncated key, dropped analysis-only
fields) are not lossy and never cause a skip.

## Warehouse-native: supported, with a few provisional edges

Warehouse-native conversion was corrected to the real Statsig shape (confirmed
against Statsig's public `semantic_layer` metric dumps and the
`terraform-provider-statsig` API models) and live-validated: `sum` and `mean`
metrics were created in a LaunchDarkly staging project bound to a Snowflake data
source, and a non-ratio `count_distinct` was created as a lossy binary. What still
needs a live warehouse-native project to fully confirm:

- **Warehouse-native ratios.** The numerator/denominator column shape follows
  Statsig's Terraform contract but hasn't been confirmed against a live
  warehouse-native ratio response.
- **Column-case sensitivity.** Warehouse column names round-trip as written; LD is
  expected to be case-insensitive here but that hasn't been exercised.
- **Full-scale creation.** Creating a whole catalog end to end requires the target
  data sources to be connected in LaunchDarkly and mapped via `--source-mapping`.

**Helping us verify:** `statsig-to-ld metrics convert --dump-raw <file>` exports
each metric's raw JSON (needs only the Statsig key). That's the artifact that lets
us confirm warehouse-native shapes against a real project. Review and redact it
before sharing (it can contain warehouse table/column names).

## How this was verified

- **Unit tests:** `go test ./...`.
- **End to end (cloud):** metrics seeded in a personal Statsig project via the
  Console API, converted with `metrics convert`, and created in a LaunchDarkly
  staging project. Created metrics were fetched back and field-checked. Covers
  cloud ratio and the windowed path.
- **End to end (warehouse-native):** warehouse-native metric definitions fed to the
  converter and created in a staging project bound to a Snowflake data source
  (`sum`, `mean`, and `count_distinct` as a binary).
- **Shape confirmation:** rollup-mode values (`max` = one-time, unset = rate) were
  confirmed with a controlled before/after on a live Statsig project; the
  warehouse-native field shapes were cross-checked against Statsig's public repos.

## Bottom line

Proven in LaunchDarkly today: base metric types, cloud ratios, windowed metrics,
and warehouse-native `sum`/`mean` (all created end to end). Unit count /
participation metrics convert cleanly for the one-time and windowed rollups; only
the daily-participation rate is lossy. Lossy features are skipped by default and
convert only with `--convert-lossy`. The main remaining gaps are unsupported types
(`percentile`, `funnel`, `composite`, `user`) and the warehouse-native ratio shape,
which is provisional until confirmed against a live warehouse-native project.
