# Statsig → LaunchDarkly metric compatibility

What `statsig-to-ld metrics convert` carries over from Statsig to LaunchDarkly,
and how thoroughly each path has been verified. Use it to set expectations before
a migration and to see which conversions are proven vs. experimental.

## Status (as of 2026-07-07)

`main` today has the base metric types, the incompatible-type skips, **cloud
ratio metrics**, and **windowed + winsorized metrics** (the windowed/winsor work
also fixed `/metrics/list` pagination, so projects with more than 100 metrics are
fully read). Two changes are still in review; this doc describes the tool **as it
behaves once they land**, and marks the rows that depend on them:

- **Skip-by-default for lossy conversions (#37)** — a conversion that would drop
  or approximate a Statsig feature is now **skipped** and reported as
  `skipped_lossy`, instead of being silently created as a degraded LaunchDarkly
  metric. `--convert-lossy` opts back in. Every ⚠️ Lossy row below reflects this.
- **Warehouse-native parsing (#36)** — teaches the converter to read
  warehouse-native metric shapes (top-level type `user_warehouse` /
  `hybrid_warehouse`, with the real aggregation under `warehouseNative`). Until it
  lands, warehouse-native metrics are skipped or failed as unknown types.
  Warehouse-native rows are marked *in review (#36)*.

## Verification tiers

| Tier | Meaning |
|---|---|
| ✅ Verified E2E | A real Statsig metric of this kind was converted by the tool and created in a LaunchDarkly staging project; the result was fetched back and field-checked. |
| 🟢 Unit-tested | Conversion logic is covered by unit tests; the LD shape was not independently created via the tool (noted where it was exercised indirectly). |
| 🟡 Partial | One side is proven, but the full Statsig→LD path can't be run in a cloud-only test project. |
| ⛔ Incompatible | No LaunchDarkly equivalent; the metric is skipped and reported (`skipped_incompatible`). |
| ⚠️ Lossy | A Statsig feature can't be represented in LaunchDarkly. The metric is **skipped by default** (`skipped_lossy`, with the dropped feature as the reason); `--convert-lossy` converts it anyway, dropping that feature and emitting a warning. |
| 🚫 Warehouse-native only | Only exists on warehouse-native Statsig metrics, which require an enterprise warehouse connection; cannot be produced or tested on a cloud (Statsig-hosted) project. |

## Metric types

| Statsig type | LaunchDarkly result | Status |
|---|---|---|
| `event_count_custom` / `event_count` | custom, non-numeric, unit aggregation `sum` | 🟢 Unit-tested¹ |
| `sum` | custom, numeric, `sum`, `eventDefault {disabled:false, value:0}` | 🟢 Unit-tested² |
| `mean` | custom, numeric, `average`, `eventDefault {disabled:true}` | 🟢 Unit-tested |
| `event_user` | custom, non-numeric, `average` | ✅ Verified E2E³ |
| `event_user_window` | same as `event_user`, with the window applied (see Windowed, below) | 🟢 Unit-tested (windowed path ✅ E2E) |
| `ratio` — cloud / event-based | LD ratio: numerator = `metricEvents[1]`, denominator = `metricEvents[0]` (Statsig stores the pair positionally, index 1 = numerator). Requires a warehouse data source, or LD rejects creation. | ✅ Created E2E; numerator/denominator direction unit-tested |
| `ratio` — warehouse-native | numerator/denominator come from warehouse config, not `metricEvents` | 🚫 Warehouse-native only · *in review (#36)* |
| ratio whose component is itself a ratio | — | ⛔ Incompatible |
| `funnel` | — (would need an LD metric group) | ⛔ Incompatible |
| `composite` / `composite_sum` | — | ⛔ Incompatible |
| `percentile` | — (LD uses percentile as an analysis type, not a metric type) | ⛔ Incompatible |
| `user` | — | ⛔ Incompatible |
| `undefined` (setup incomplete) | — | ⛔ Incompatible |

## Features & modifiers (orthogonal to type)

| Feature | Statsig source | LaunchDarkly mapping | Status |
|---|---|---|---|
| Directionality | `increase` / `decrease` | `HigherThanBaseline` / `LowerThanBaseline` | ✅ |
| Randomization unit | `unitTypes` (e.g. `userID`) | `randomizationUnits` (`user`); override via `--unit-type-mapping` | ✅ |
| Windowed (custom rollup) | `rollupTimeWindow=custom`, `customRollUpStart/End` (days) | `windowStartOffset` / `windowEndOffset` (ms) **when a data source is bound** | ✅ Verified E2E (bound); ⚠️ Lossy when no data source (LD windows are snowflake-experimentation only) |
| Winsorization | `warehouseNative.winsorizationLow/High` (fractions, 0–1) | `winsorLowerPercentile` / `winsorUpperPercentile` (0–100) on numeric metrics | 🟡 conversion unit-tested + LD accepts the shape (verified); ⚠️ Lossy on occurrence metrics (LD can't winsorize those) |
| Count distinct — ratio term (no column) | `metricEvents[i].type=count_distinct`, no column | cloud = distinct users → LD **binary** metric (non-numeric, `average`); a faithful mapping, no warning | 🟢 Unit-tested |
| Count distinct — ratio term (named column) | `metricEvents[i].type=count_distinct` + column | `count_distinct` + `unitAggregationField` | 🚫 Warehouse-native only |
| Count distinct — simple metric | `metricEvents[0].type=count_distinct` | not carried over (LD would count all occurrences) | ⚠️ Lossy |
| Metadata aggregation | `metricEvents[0].type=metadata` | not carried over (LD aggregates the tracked value) | ⚠️ Lossy |
| Event filters / criteria | `metricEvents[0].criteria` | **data loss** — filters not applied; the LD metric would match all events | ⚠️ Lossy |
| Multiple metric events | more than one entry in `metricEvents` | only the first event is used; the rest are dropped | ⚠️ Lossy |
| Per-unit capping | `warehouseNative.cap` | unsupported in LD | ⚠️ Lossy · 🚫 source |
| Log transform | `warehouseNative.useLogTransform` | unsupported in LD | ⚠️ Lossy · 🚫 source |
| Daily participation rate | `rollupTimeWindow=daily_participation_rate` | no faithful LD equivalent (a binary metric would lose the per-day rate) | ⚠️ Lossy⁴ |

**About the ⚠️ Lossy rows:** by default these metrics are **skipped** and listed in
`migration-report.json` as `skipped_lossy`, with the dropped feature as the reason.
Re-run with `--convert-lossy` to convert them anyway — the conversion then goes
through with the feature dropped and the same reason surfaced as a warning. Purely
advisory notes (e.g. a non-standard unit type, a truncated key) are **not** lossy
and never cause a skip.

¹ The `count` term shape was emitted by the converter and accepted by LD as ratio terms during the ratio E2E. ² The `sum` shape was confirmed accepted by LD via a direct winsorization create. ³ The windowed E2E metric is an `event_user` metric — its successful creation confirms the base `event_user` mapping. ⁴ Once warehouse-native parsing (#36) lands, a warehouse-native metric whose *aggregation* is `daily_participation` is treated as ⛔ Incompatible (skipped outright); the ⚠️ Lossy row here is the cloud/rollup-window form.

## The warehouse-native wall (experimental / not testable on a cloud project)

Several Statsig features exist **only on warehouse-native metrics**, which require
an enterprise warehouse (e.g. Snowflake) connection to create. On a cloud
(Statsig-hosted) project the Console API returns `warehouseNative: null` and drops
these fields, so they can't be produced or exercised end to end from our side:

- **Warehouse-native ratios** — numerator and denominator live in warehouse
  config, not `metricEvents`. Parsing and routing for these is **in review (#36)**;
  the field mappings follow Statsig's API reference but haven't been confirmed
  against a live warehouse-native response yet.
- **Winsorization (full Statsig→LD path)** — the conversion and LD's acceptance of
  the emitted shape are each proven independently; the joined run is untestable
  because Statsig only stores winsorization on warehouse-native metrics.
- **Count distinct over a named column** (ratio terms) — cloud ratios carry no
  column (their count_distinct is distinct-users, mapped to a binary metric), so
  only the binary path is exercised; the named-column path is warehouse-native.
- **Per-unit capping / log transform** — warehouse-native only in Statsig, and
  unsupported in LaunchDarkly regardless.

**Helping us verify:** if you have an enterprise warehouse-native Statsig project,
`statsig-to-ld metrics convert --dump-raw <file>` exports each metric's raw JSON
(needs only the Statsig key) — that's the artifact that lets us confirm the
warehouse-native field mappings. Review and redact it before sharing (it can
contain warehouse table/column names).

## How this was verified

- **Unit tests:** `go test ./...`.
- **End to end:** metrics seeded in a personal Statsig (cloud) project via the
  Console API, converted with `metrics convert`, and created in a LaunchDarkly
  **staging** project — including one bound to a `snowflake-experimentation` data
  source for the windowed case. Created metrics were fetched back and their fields
  checked against the source.
- **Not testable from our side:** anything in the warehouse-native section above,
  which is gated behind an enterprise Statsig warehouse connection.

## Bottom line

Proven in LaunchDarkly today: **windowed metrics** (fully end to end) and **cloud
ratios** (created end to end; numerator/denominator direction unit-tested).
Solidly covered: the base metric types and **winsorization** (both halves verified,
just not joined). Lossy features are **skipped by default** and convert only with
`--convert-lossy`. Everything in the warehouse-native section — including
warehouse-native ratios now being added in #36 — should be treated as experimental
until a warehouse-native Statsig project is available to test against.
