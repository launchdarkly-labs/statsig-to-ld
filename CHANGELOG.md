# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `metrics convert` and `warehouse`: `--ld-maintainer` sets the maintainer on every created metric and metric data
  source. LaunchDarkly fills this in automatically from the token's member, but only for a personal token: a
  service token is not tied to a member, so resources it created were left unmaintained and the LaunchDarkly UI
  flagged them as incomplete. The CLI now resolves a maintainer once per run and sets it explicitly, so both token
  kinds produce the same result. The flag takes an email, a 24-character member ID, or `none` to create resources
  with no maintainer; omitted, it uses the member who owns the API token. Resolution failure stops the run before
  anything is created rather than quietly leaving hundreds of resources unmaintained, and the error names the flag
  as the fix. The resolved maintainer is printed once at the start of the run.

- `metrics convert`: `--assume-constant-event-key` treats mapped data sources that LaunchDarkly cannot report on (no
  credentials, or not created yet) as projecting the constant event key, so a dry run before `warehouse` previews the
  event keys a real run will use. Their columns are unknown, so value, count-distinct, and filter column case cannot
  be checked; the run summary counts the affected metrics in one line, and each carries the note code
  `column_unverified` in the report's new per-metric `note_codes`. The report's `options` block records the flag as
  `assume_constant_event_key`, the data sources it applied to as `assumed_constant_key_data_sources`, with
  `data_sources_fetched` (whether the data sources were read from LaunchDarkly) and `constant_key_data_sources` (how
  many of them project the constant).

- `warehouse`: `--update-mappings` corrects the timestamp, value, and context mappings of existing metric data
  sources in place from the Statsig export, keeping their keys, queries, key columns, column lists, and bound
  metrics, on data sources with or without the constant event key. Each mapping the export defines itself (its
  timestamp column, its id-type mappings, and a value column only when it names one) is matched case-insensitively
  to the data source's listed columns, and only fields that differ are patched, after the same `test` ops as
  `--overwrite`; a data source that already matches gets no PATCH. Context kinds are only added or moved to another
  column, never removed: a kind the export does not map, such as one renamed by hand in LaunchDarkly, is kept (note
  code `context_kind_not_in_export`), and an export kind is added even when another kind maps the same column. A
  mapping is left as it is when the data source's columns do not include the export's column
  (`export_column_not_in_query`), when the column's type does not fit (a timestamp column needs a timestamp or date
  type and a value column a numeric one; a column with no type passes; `export_column_wrong_type`), or, for a value
  column the export no longer maps, while bound numeric metrics without their own value column read it
  (`value_column_kept`); each prints one line per run naming the data sources. With `--overwrite`, a data source
  without the constant event key still gets the wrapped query and new column list, but takes its mappings from the
  export instead of keeping its old ones. Every update, with `--overwrite`, `--update-mappings`, or both, is refused,
  naming the metrics, when the patch it would send removes a value column bound numeric metrics read or a context
  kind bound metrics use as an analysis or randomization unit. A metric is bound through its numerator's data source
  or its denominator's, and a denominator with no data source of its own reads the numerator's, which
  `--overwrite`'s event-key check now counts too. `--overwrite` also checks the type of a fallback timestamp or value
  column. A `--dry-run` with LD credentials prints each changed field (`timestamp: TS → CREATED_AT`), and the report
  records each update as note code `mappings_updated` with a `changes` list in the same form, which `--overwrite`'s
  warnings now use too.


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

- `warehouse` and `metrics convert`: warehouse-native metrics now match their rows. LaunchDarkly requires an event
  key column on every warehouse data source and filters each metric on `key column = eventKey`, but Statsig metric
  sources have no event key. Data source creation failed with "Event key column is required" whenever the CLI could
  not guess a key column from the Statsig field names, and converted metrics used the value column or source name as
  their event key, which matched no rows. `warehouse` now wraps each source's SQL as
  `SELECT *, '<data source key>' AS LD_EVENT_KEY FROM (<source SQL>) AS ld_src` (`ld_event_key` on BigQuery,
  Databricks, and Redshift; on Redshift the literal is cast to `VARCHAR(256)`, since Redshift types an uncast
  literal in a subquery as `unknown`), previews the wrapped query so the saved columns match it exactly, and makes that column
  the key column. `metrics convert` reads each data source back from LaunchDarkly; for one that projects the constant
  (its key column is the projected column, and its whole query is that wrapper, ignoring comments, or still opens
  with `SELECT *, '<its own key>' AS <key column> FROM (` after an edit in LaunchDarkly; a hand-built `UNION` of
  literal-tagged subqueries does not count), it sets every warehouse-native metric's event key (and a ratio
  denominator's event name) to the data source key, recorded as note code `constant_event_key` rather than a warning,
  sends the Statsig value column as `valueColumn` on numeric terms, and rewrites value, count-distinct, and filter
  columns to the case the data source stores them in. When a mapped data source exists without the constant, one line
  names it and says to run `warehouse --overwrite` first. `--constant-event-key=false` restores the old behavior. Source
  SQL that cannot be nested (more than one statement, a Statsig date macro, or no SQL or table at all) fails that
  source with the reason. A `warehouse --dry-run` writes the wrapped bodies to `data-source-bodies.json` for review.

- `metrics convert`: a warehouse-native metric on a constant-key data source whose Statsig filter criteria do not all
  convert is no longer created, even with `--convert-lossy`. On such a source the event key matches every row, so the
  filter is the only thing selecting rows and the metric would count the whole source. It is reported as
  `skipped_incompatible` with blocking code `constant_event_key_unfiltered`, which the CSV report carries in a new
  last column, `blocking_codes`.

- `warehouse`: `--overwrite` now updates an existing data source that lacks the constant event key in place instead
  of skipping it. LaunchDarkly has no way to delete a metric data source and keeps keys unique even after archiving,
  so this is how a data source created before the constant event key gets one. The update changes only the query, the
  key column, and the column list; the timestamp, value, and context mappings are kept while the new query returns
  them. It is a JSON Patch that first tests the query and column mappings the run listed, so a data source changed in
  LaunchDarkly during the run fails ("changed in LaunchDarkly after this run read it; rerun to pick up the change")
  instead of being overwritten. A data source that already projects the constant is never rewritten, whatever the
  state file says (the run warns when its Statsig SQL has changed), nor is one whose wrapper was edited in
  LaunchDarkly (skipped as "kept: edited in LaunchDarkly", without a warning), and `--constant-event-key=false` never
  removes the constant. Before updating, the run reads the project's metrics (by offset, checked against the total
  count and read once more if short) and refuses a data source whose bound metrics (through the numerator or a
  ratio's denominator) use an event key other than the data source key, since they would match no rows afterwards;
  `--force-overwrite` updates it anyway and lists them. If the metrics cannot be read completely, nothing is updated.
  With `--constant-event-key=false`, an update keeps the existing key column instead, and fails the source when the
  new query does not return it. A source whose key is taken by a data source in another LaunchDarkly environment
  fails, with or without `--overwrite`, and is left out of `source-mapping.json`. Without `--overwrite`, existing data
  sources that lack the constant are skipped, and the run prints one line counting them and saying what
  `--overwrite` would do; the report's new `notes` list names them. A `--dry-run` with `--ld-key` and `--ld-project`
  reports, per source, whether a real run would create, update, skip, or refuse it.

- `warehouse`: data source context kinds are now named the way `metrics convert` names analysis units without
  `--unit-type-mapping`, so a data source has the kinds its metrics analyze by: `userID` is `user`, and any other
  Statsig unit ID is lowercased. Before, every unit ID containing "user" (such as `anonymousUserID` or `user_id`)
  became `user`, the last one silently winning, and other unit IDs were sanitized like keys. A new data source now
  gets `anonymoususerid` or `user_id` for those; `--update-mappings` adds them to an existing one, moving `user` to
  the `userID` column if another unit ID had taken it. A source whose unit IDs still map to one kind with different columns (for
  example `userID` and `user`) fails, naming them, on create and with `--update-mappings`; a plain `--overwrite` fails it
  only when its fallback would take that kind's column from the export. Kinds that share a column all stay on create;
  before, one of them was dropped at random. `warehouse` takes no
  unit-type mapping, so a kind renamed with `metrics convert --unit-type-mapping` must be renamed on the data source in
  LaunchDarkly too.

- `metrics convert`: `--widen-analysis-units` now defaults to **off**. Real data settled it: in a customer's
  warehouse export, 60 of 100 metric sources map two or more id types and one maps nine, so widening is not a
  marginal change, and every unit it adds has to be registered on the LaunchDarkly project. Clustered analysis is
  also gated on the LaunchDarkly side, so a widened list does nothing until that is enabled. Pass
  `--widen-analysis-units` to opt in.

### Fixed

- `warehouse` and `metrics convert`: a failed read of the project's metric data sources was treated as "none exist".
  `warehouse` then posted every source again, and `metrics convert` fell back to legacy event keys. A real run of
  either now stops with the error; a `metrics convert` dry run warns and continues.

- `warehouse`: the warehouse preview's column guesses (the first timestamp-typed column as the timestamp column; an
  `event_value` column, else the first numeric column, as the value column) replaced the columns configured in
  Statsig, and a data source got the value-column guess even when its Statsig source defined none. Statsig's timestamp
  column now wins whenever the warehouse returns it, in the warehouse's case, and the value-column guess is never
  used: a data source gets a value column only when its Statsig source defines one and the query returns it. The run
  prints one line naming the data sources whose query does not return their Statsig value column (note code
  `statsig_value_column_not_in_query`). `--overwrite` no longer puts the guess in place of an existing value column
  the new query lacks, and refuses a data source whose bound numeric metrics read that column without a value column
  of their own, naming them. Numeric metrics then take their value column from the metric, which `metrics convert`
  sets on constant-key data sources; on one created with `--constant-event-key=false`, a numeric metric needs a value
  column set by hand.

- `warehouse`: Statsig sources whose names sanitize to the same LaunchDarkly key (for example `Checkout Events` and
  `checkout events`) were written to one data source, the last one winning, and `source-mapping.json` bound all of
  them to it. Every source in such a group now fails with an error naming the others, and none of them is created,
  updated, or written to `source-mapping.json`.

- `warehouse --resume`: a data source recorded in `migration_state.json` skipped the check against LaunchDarkly, so
  one that predated the constant event key was never flagged or updated.

- `metrics convert`: a warehouse-native ratio term with a `count_distinct` aggregation dropped its column and
  converted as a binary count of units. It now converts as `count_distinct` on that column.

- `metrics convert`: a metric that already exists is skipped. When it reads a constant-key data source but its event
  key is not the data source key, it matches no rows; it is now named in the report and the run summary (code
  `existing_metric_event_key_mismatch`) instead of counted as a quiet skip. One that cannot be read back is coded
  `existing_metric_unverified` and is not counted as a mismatch.

- `metrics convert`: a cloud metric bound to a constant-key data source (for example through `--ld-data-source`)
  keeps its event name, which never matches the constant. It now carries a `cloud_metric_on_constant_key_source`
  warning.

- `metrics convert`: a custom rollup window converted with its end day one day short. Statsig counts window days
  inclusively (days 0-6 is 7 days of data), while LaunchDarkly's window is a duration from first exposure, so a
  Statsig end day of N must map to an offset of N+1 days. The converter multiplied the end day straight through, so
  a Statsig 0-6 window became a 6-day LaunchDarkly window instead of 7. End offsets are now one day longer; start
  offsets are unchanged.

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
