# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- **Breaking:** Binary renamed from `statsig-metric-importer` to `statsig-to-ld` to reflect the multi-subcommand direction.
- **Breaking:** Metric subcommand renamed from `convert` to `metric-import` so additional subcommands can sit alongside without ambiguity. Old invocation now errors with `unknown command "convert" for "statsig-to-ld"` to surface the change on first run.
- HTTP `User-Agent` header is now `statsig-to-ld/<version>`.

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

[Unreleased]: https://github.com/launchdarkly-labs/statsig-metric-importer-cli/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/launchdarkly-labs/statsig-metric-importer-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/launchdarkly-labs/statsig-metric-importer-cli/releases/tag/v0.1.0
