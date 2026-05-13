# GitHub Copilot instructions — statsig-to-ld

This repository is a Go CLI (`statsig-to-ld`) that migrates flag and metric definitions and targeting rules from Statsig to LaunchDarkly.

The complete, agent-agnostic operator guide is at [`AGENTS.md`](../AGENTS.md) in the repo root. Treat it as authoritative when helping a user run, build, or troubleshoot the CLI. It covers build, API-key handling, the recommended migration sequence, per-subcommand usage (`analyze`, `flags import`, `targeting import`, `metrics convert`), report analysis, and troubleshooting.
