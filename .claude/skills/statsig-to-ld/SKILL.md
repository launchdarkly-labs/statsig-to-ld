---
name: statsig-to-ld
description: Use when running, building, or troubleshooting the `statsig-to-ld` CLI — a tool that migrates flag definitions, targeting rules, and metric definitions from Statsig to LaunchDarkly. Triggers include Statsig→LaunchDarkly migration, the subcommands `analyze` / `flags import` / `targeting import` / `metrics convert`, API key setup for `STATSIG_CONSOLE_KEY` and `LD_API_KEY`, interpreting `migration-report.json` / `flag-import-report.json` / `targeting-import-report.json`, and lossy-targeting opt-ins via `--accept-data-loss`.
---

The complete operator guide for the `statsig-to-ld` CLI is the agent-agnostic `AGENTS.md` at the repo root. Read and follow it as the authoritative source for build, key handling, migration sequencing, per-subcommand usage, report analysis, and troubleshooting.

@../../../AGENTS.md
