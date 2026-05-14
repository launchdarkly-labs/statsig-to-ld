# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/launchdarkly-labs/statsig-to-ld/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/launchdarkly-labs/statsig-to-ld/releases/tag/v0.1.0
