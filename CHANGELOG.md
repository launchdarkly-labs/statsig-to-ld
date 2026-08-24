# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `metrics convert`: analysis units are now checked against the target LaunchDarkly project before anything is
  created. LaunchDarkly only accepts a unit that is registered as a randomization unit on the project, so a metric
  naming anything else is rejected outright. The command reads the project's experimentation settings once and drops
  unregistered units with a warning (code `analysis_unit_not_registered`) rather than letting the create fail. The
  lookup is read-only, so it also runs on a `--dry-run` when `--ld-key` and `--ld-project` are supplied, which is
  where you want to find out. Without credentials, or if the lookup fails, nothing is filtered: narrowing a metric's
  analysis units because a lookup failed would be a worse outcome than the rejection it avoids.

- `metrics convert`: the migration report gained an `options` block recording the settings that shaped the run
  (`convert_lossy`, `widen_analysis_units`, `extra_analysis_units`, `ld_data_source`, `source_mapping_entries`,
  `unit_type_mapping_entries`, `metric_sources_fetched`, `registered_analysis_units`). A returned report was
  previously ambiguous: zero widened metrics read the same whether widening was off, the metric-source lookup
  failed, or no source had anything to add.

### Changed

- `metrics convert`: `--widen-analysis-units` now defaults to **off**. Real data settled it: in a customer's
  warehouse export, 60 of 100 metric sources map two or more id types and one maps nine, so widening is not a
  marginal change, and every unit it adds has to be registered on the LaunchDarkly project. Clustered analysis is
  also gated on the LaunchDarkly side, so a widened list does nothing until that is enabled. Pass
  `--widen-analysis-units` to opt in.

### Fixed

- `metrics convert`: `--extra-analysis-units` was applied after the fallback that defaults a metric with no
  resolvable unit to `user`, so passing it silently replaced that fallback and suppressed its warning. Extras are
  now added alongside the fallback instead of standing in for it.

- `metrics convert`: widening is documented as inert for Statsig Cloud metrics, but the only guard was in the
  command layer. A cloud metric carrying a top-level `metricSourceName` was widened anyway on a run that had also
  loaded warehouse sources. The converter now requires the metric to be warehouse-native.

- `metrics convert`: a warehouse-native ratio was widened using only its numerator's source, so it could claim an
  analysis unit its denominator has no column for. Widening a ratio now adds only units both sources declare, and
  adds nothing when the denominator's source is unknown.

- `metrics convert`: the hint shown when LaunchDarkly rejects an unregistered unit said to add it as a context kind,
  which is necessary but not sufficient. It now also says to enable the kind for experiments, which is configured
  separately.

- `warehouse`: the command read only the first page of Statsig metric sources, so any account with more than 100
  silently lost the rest. The count looked plausible, and the missing sources were absent from the export, from the
  "data sources that would be created" preview, and from the generated `source-mapping.json`. A metric whose source
  fell off the end then resolves to no data source in `metrics convert`, which quietly means its filters stay lossy,
  its measurement window is dropped, and a ratio fails outright. `ListMetricSources` now follows pagination, matching
  what `metrics convert` already did.

- `warehouse`: the warehouse type was reported as a detection when it was often a guess, and the guess was
  unreliable. When Statsig does not expose its warehouse connection config, the type was inferred by substring
  matching metric source SQL against tokens including `DELTA`, `::`, and a bare backtick. `DELTA` matches any
  identifier containing "delta", `::` is a cast in several dialects as well as the separator in Statsig's own metric
  IDs, and a backtick is ordinary quoting, so a run could confidently report the wrong warehouse. The answer also
  depended on which source Statsig happened to return first. Three changes:
  - New `--warehouse-type` flag (`snowflake`, `bigquery`, `databricks`, `redshift`) takes precedence over everything.
  - The ambiguous tokens are gone, and the remaining markers are checked across every source rather than stopping at
    the first match. Disagreement between sources now yields no guess instead of an arbitrary winner.
  - The type's provenance is tracked and shown. Nothing is created from an unconfirmed guess, because the type
    selects the LaunchDarkly integration key and getting it wrong binds every data source to the wrong warehouse.
    The dry-run report labels a guess as a guess.

- `warehouse`: the warehouse-type prompt looped forever on EOF, so a non-interactive run that needed to confirm the
  type would spin instead of failing. It now returns an error naming `--warehouse-type`, and it validates the menu
  selection properly rather than accepting anything that string-compares between "1" and "4".

### Added

- `metrics convert`: Statsig filter criteria on warehouse-native metrics now convert to LaunchDarkly metric filters
  instead of being dropped. Ratio metrics carry a filter per term, so the numerator and denominator convert
  independently. Mapped conditions: `in`, `=`, `not_in`, `contains`, `not_contains`, `starts_with`, `ends_with`,
  `>`, `>=`, `<`, `<=`, `non_null`, `is_null`, `is_true`, `is_false`. Conversion is all-or-nothing per term: if any criterion is unmappable
  the term keeps no filter and stays lossy, because criteria are AND-ed and applying a subset would widen what the
  metric matches. Requires a bound data source, and filters currently compute only on Snowflake-backed sources.

- `metrics convert`: the migration report now carries machine-readable diagnostics per metric, so a run can be
  analysed without pattern-matching warning text. New fields: `warning_codes` (parallel to `warnings`),
  `lossy_reasons` and `lossy_codes`, `ld_data_source`, `analysis_units`, `statsig_rollup_time_window`,
  `statsig_source_name`, and `filters` (one entry per metric term with its criteria count, whether a filter was
  applied, and if not, `blocked_by` plus the responsible `blocked_condition`). The CSV output gains the flat
  equivalents plus `filters_applied` / `filters_blocked`. `AGENTS.md` has `jq` recipes for all of them.

- `metrics convert`: support for experiments that analyze a metric by a different unit than they randomize on —
  Statsig's clustered experiments. Converted metrics carry the full set of units they can be analyzed by, so the
  unit can be selected per metric when the experiment is created. `--widen-analysis-units` (default on) adds the id
  types a metric's Statsig source declares; `--extra-analysis-units` adds LD context kinds directly.

### Changed

- `metrics convert`: metric payloads now use LaunchDarkly's `analysisUnits` field instead of the deprecated
  `randomizationUnits`. Same list, current name. The migration report's matching field and CSV column are
  `analysis_units` for the same reason.
### Changed

- `metrics convert`: a Statsig warehouse-native `count_distinct` metric that counts a column now converts to a real
  LaunchDarkly `count_distinct` metric instead of being approximated as a binary one. LaunchDarkly has since added
  support for the aggregation on simple (non-ratio) metrics, so the approximation is no longer necessary and these
  metrics are no longer lossy. The counted column travels in `unitAggregationField`, and the metric is numeric
  because the per-unit value is a distinct count. Two cases are unchanged: counting distinct **units** (no column)
  still converts to a binary metric, which expresses exactly the same thing, and a ratio metric's terms still stay
  non-numeric. A data source is still required, since LaunchDarkly accepts the aggregation only on warehouse-native
  metrics; without one the metric falls back to the binary approximation and stays lossy.

### Fixed

- `metrics convert`: a skipped-lossy metric recorded only its lossy reasons, discarding every advisory warning on
  it. Since a skipped metric is the one most likely to need triage, that hid useful context (the resolved analysis
  unit, for instance) on exactly the wrong metrics. The report now keeps the full `warnings` list alongside the
  `lossy_reasons` subset.
- `metrics convert`: a ratio metric whose term filter criteria were dropped reported as a clean conversion instead of
  lossy, so it could be created in LaunchDarkly matching every row rather than the filtered subset. Dropped ratio-term
  criteria now mark the conversion lossy, matching the non-ratio path, and the warning lists the dropped criteria.

## [0.2.1] - 2026-06-05

### Fixed

- **`metrics convert`**: Statsig `mean` metrics now map to LaunchDarkly with `eventDefault.disabled = true`, so units exposed to the experiment but without recorded events are excluded from the analysis — matching Statsig's `SUM(value) / SUM(records)` group-level formula. Previously the converter imputed 0 for missing units, which silently changed the metric from "average per event-emitting unit" to "average per exposed unit." `sum` and the binary/count metric mappings are unchanged. ([#30](https://github.com/launchdarkly-labs/statsig-to-ld/pull/30))

## [0.2.0] - 2026-05-12

This release renames the project from `statsig-metric-importer` to `statsig-to-ld` and expands it from a metrics-only tool to a full Statsig→LaunchDarkly migration CLI. The metric importer is unchanged; three new subcommands cover flag and targeting import.

### Added

- **`statsig-to-ld analyze`** — read-only sizing report. Surveys gates, dynamic configs, environments, and metrics; classifies each by how the importer will treat it. Use before any import to scope the migration.
- **`statsig-to-ld flags import`** — creates LaunchDarkly flag shells from Statsig feature gates and dynamic configs. Idempotent re-runs via key-based dedupe. `--include-tag`, `--ld-tag`, `--ld-maintainer`, `--dry-run`, parallel creation with `--concurrency`.
- **`statsig-to-ld targeting import`** — applies per-environment targeting rules, rollouts, and user/context targets to flag shells. **Fail-closed by default** on lossy transformations (Statsig segments, gate prerequisites, custom unit IDs, multi-variant DC overrides, unreachable trailing rules). Opt in with `--accept-data-loss=all` or a comma-separated list. Auto-creates missing LD envs (disable with `--no-create-envs`).
- **Migration playbook** at `docs/migration-playbook.md` covering what the CLI does **not** do: SDK call-site rewrites, Statsig segments recreation, gate prerequisites, layers, experiments, holdouts, cutover sequencing, validation strategy, rollback.
- API client coverage for the LaunchDarkly REST flag, environment, and JSON Patch endpoints (`launchdarkly.ListAllFlags`, `CreateFlag`, `ListEnvironments`, `CreateEnvironment`, `PatchFlag`).
- API client coverage for the Statsig gate, dynamic config, environment, and override endpoints (`statsig.ListGates`, `ListDynamicConfigs`, `ListEnvironments`, `GetGateOverrides`, `GetDynamicConfigOverrides`).
- Test coverage: command-tree resolution, flag binding, and `--help` rendering at every level (`cmd/cmd_test.go`).

### Changed

- **Renamed binary** from `statsig-metric-importer` to `statsig-to-ld`.
- **Restructured commands**: `statsig-metric-importer convert ...` is now `statsig-to-ld metrics convert ...`. The existing `convert` command is unchanged in behavior; only its location in the command tree has moved to make room for `flags import`, `targeting import`, and `analyze`.
- Module path moved from `github.com/launchdarkly-labs/statsig-metric-importer-cli` to `github.com/launchdarkly-labs/statsig-to-ld`.

### Known limitations (v0.2.0)

- **Re-running `targeting import` overwrites manual LD edits** without a diff preview. A future release will add `--update-existing --diff` for safe re-runs.
- **Statsig segments are not auto-recreated in LD**. Targeting rules that reference segments are skipped by default (or dropped with `--accept-data-loss=segments`). A future release will add `segments export` to dump definitions for hand-recreate.
- **Statsig layers, experiments, and holdouts** are not addressed. See the migration playbook for guidance.
- **Single context kind in targeting**: rules and overrides are emitted under the `user` context regardless of Statsig unit type. Custom unit IDs are flagged in the report; re-map in LD if needed.

## [0.1.1] - 2026-05-06

### Added
- Actionable hints for LaunchDarkly API `401`, `403`, and `404` errors so users can self-diagnose auth and scoping problems.

### Changed
- `--unit-type-mapping` hint now clarifies that the flag takes a file path, not inline JSON.
- Unit-type mapping lookup is now case-insensitive on the key.

### Fixed
- Improved error UX for two common migration failures.

## [0.1.0] - 2026-05-01

### Added
- Initial release of the Statsig metric importer CLI.
- Converts Statsig metric definitions (Statsig Cloud and Warehouse Native) into LaunchDarkly metrics.
- Idempotent re-runs, parallel processing, and structured migration reports.
- CI and release workflow.

[Unreleased]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/launchdarkly-labs/statsig-to-ld/releases/tag/v0.1.0
