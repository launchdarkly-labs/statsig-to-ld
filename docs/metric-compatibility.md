# Statsig → LaunchDarkly metric compatibility

What `statsig-to-ld metrics convert` carries over from Statsig to LaunchDarkly,
and how thoroughly each path has been verified. Use it to set expectations before
a migration and to see which conversions are proven vs. experimental.

## Status of the underlying work (as of 2026-07-06)

Some capabilities below are still in review on separate branches and are **not yet
on `main`**:

- **Ratio metrics** — in review (PR into the ratio feature branch, #33 → #32).
- **Windowed + winsorized metrics** — in review (PR #34), which also fixes
  `/metrics/list` pagination so projects with more than 100 metrics are fully read.

`main` today has the base metric types, the incompatible-type skips, and the
lossy-feature warnings. The tables describe the **combined** intended state and
mark each row's source (`main` / `ratio PR` / `windowed+winsor PR`) so it's clear
what's live vs. in review.

## Verification tiers

| Tier | Meaning |
|---|---|
| ✅ Verified E2E | A real Statsig metric of this kind was converted by the tool and created in a LaunchDarkly staging project; the result was fetched back and field-checked. |
| 🟢 Unit-tested | Conversion logic is covered by unit tests; the LD shape was not independently created via the tool (noted where it was exercised indirectly). |
| 🟡 Partial | One side is proven, but the full Statsig→LD path can't be run in a cloud-only test project. |
| ⛔ Incompatible | No LaunchDarkly equivalent; the metric is skipped and reported (`skipped_incompatible`). |
| ⚠️ Lossy | The metric converts, but this feature is dropped and a warning is emitted. |
| 🚫 Warehouse-native only | Only exists on warehouse-native Statsig metrics, which require an enterprise warehouse connection; cannot be produced or tested on a cloud (Statsig-hosted) project. |

## Metric types

| Statsig type | LaunchDarkly result | From | Status |
|---|---|---|---|
| `event_count_custom` / `event_count` | custom, non-numeric, unit aggregation `sum` | main | 🟢 Unit-tested¹ |
| `sum` | custom, numeric, `sum`, `eventDefault {disabled:false, value:0}` | main | 🟢 Unit-tested² |
| `mean` | custom, numeric, `average`, `eventDefault {disabled:true}` | main | 🟢 Unit-tested |
| `event_user` | custom, non-numeric, `average` | main | ✅ Verified E2E³ |
| `event_user_window` | same as `event_user` (window applied via custom rollup — see below) | main | 🟢 Unit-tested |
| `ratio` — cloud / event-based | LD ratio: numerator = `metricEvents[0]`, denominator = `metricEvents[1]` | ratio PR | ✅ Verified E2E |
| `ratio` — warehouse-native | numerator/denominator are not in `metricEvents`; converter fails loudly instead of mis-converting | ratio PR | 🚫 Warehouse-native only (unimplemented + untestable) |
| ratio whose component is itself a ratio | — | ratio PR | ⛔ Incompatible |
| `funnel` | — | main | ⛔ Incompatible |
| `composite` / `composite_sum` | — | main | ⛔ Incompatible |
| `percentile` | — | main | ⛔ Incompatible |
| `user` | — | main | ⛔ Incompatible |
| `undefined` (setup incomplete) | — | main | ⛔ Incompatible |

## Features & modifiers (orthogonal to type)

| Feature | Statsig source | LaunchDarkly mapping | From | Status |
|---|---|---|---|---|
| Directionality | `increase` / `decrease` | `HigherThanBaseline` / `LowerThanBaseline` | main | ✅ |
| Randomization unit | `unitTypes` (e.g. `userID`) | `randomizationUnits` (`user`); override via `--unit-type-mapping` | main | ✅ |
| Windowed (custom rollup) | `rollupTimeWindow=custom`, `customRollUpStart/End` (days) | `windowStartOffset` / `windowEndOffset` (ms) **when a data source is bound**; otherwise a warning (LD windows are snowflake-experimentation only) | windowed+winsor PR | ✅ Verified E2E |
| Winsorization | `warehouseNative.winsorizationLow/High` (fractions, 0–1) | `winsorLowerPercentile` / `winsorUpperPercentile` (0–100); skipped with a warning on occurrence metrics | windowed+winsor PR | 🟡 conversion unit-tested + LD accepts the shape (verified); source side 🚫 (see below) |
| Count distinct — ratio term | `metricEvents[i].type=count_distinct` | No column (cloud = distinct users) → LD **binary** metric (non-numeric, `average`), i.e. count distinct of the analysis unit — a faithful mapping, no warning. Named column → `count_distinct` + `unitAggregationField` (warehouse-native only in LD) | ratio PR | 🟢 binary/no-column path unit-tested; named-column path 🚫 |
| Count distinct — simple metric | `metricEvents[0].type=count_distinct` | not carried over; warns that LD counts all occurrences | main | ⚠️ Lossy |
| Metadata aggregation | `metricEvents[0].type=metadata` | not carried over; warns (LD aggregates the tracked value) | main | ⚠️ Lossy |
| Event filters / criteria | `metricEvents[0].criteria` | **data loss** — not applied; the LD metric matches all events; warns | main | ⚠️ Lossy |
| Per-unit capping | `warehouseNative.cap` | unsupported in LD; warns | main | ⚠️ Lossy + 🚫 source |
| Log transform | `warehouseNative.useLogTransform` | unsupported in LD; warns | main | ⚠️ Lossy + 🚫 source |
| Daily participation rate | `rollupTimeWindow=daily_participation_rate` | not carried over; warns (standard binary conversion) | main | ⚠️ Lossy |

¹ The `count` term shape was emitted by the converter and accepted by LD as a ratio numerator/denominator during the ratio E2E. ² The `sum` shape was confirmed accepted by LD via a direct winsorization create. ³ The windowed E2E metric is an `event_user` metric — its successful creation confirms the base `event_user` mapping.

## The warehouse-native wall (experimental / not testable on a cloud project)

Several Statsig features exist **only on warehouse-native metrics**, which require
an enterprise warehouse (e.g. Snowflake) connection to create. On a cloud
(Statsig-hosted) project the Console API returns `warehouseNative: null` and drops
these fields, so they can't be produced or exercised end to end:

- **Warehouse-native ratios** — numerator/denominator live in warehouse config,
  not `metricEvents`. The converter does not handle that shape yet; it fails loudly
  (never silently mis-converts). Unimplemented and unverified.
- **Winsorization (full Statsig→LD path)** — the conversion and LD's acceptance of
  the emitted shape are each proven independently; the joined run is untestable
  because Statsig only stores winsorization on warehouse-native metrics.
- **Count distinct over a named column** (ratio terms) — cloud ratios carry no
  column (their count_distinct is distinct-users, mapped to a binary metric), so
  only the binary path is exercised; the named-column path is warehouse-native
  and unverified.
- **Per-unit capping / log transform** — warehouse-native only in Statsig, and
  unsupported in LaunchDarkly regardless.

## How this was verified

- **Unit tests:** `go test ./...`.
- **End to end:** metrics seeded in a personal Statsig (cloud) project via the
  Console API, converted with `metrics convert`, and created in a LaunchDarkly
  **staging** project — including one bound to a `snowflake-experimentation` data
  source for the windowed case. Created metrics were fetched back and their fields
  checked against the source.
- **Not testable here:** anything in the warehouse-native section above, which is
  gated behind an enterprise Statsig warehouse connection.

## Bottom line

Fully proven end to end today: **cloud ratios** and **windowed metrics**. Solidly
covered: the base metric types and **winsorization** (both halves verified, just
not joined). Everything in the warehouse-native section should be treated as
experimental until a warehouse-native Statsig project is available to test against.
