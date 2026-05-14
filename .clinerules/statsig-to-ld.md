# statsig-to-ld — operator guide

The complete, agent-agnostic operator guide for the `statsig-to-ld` CLI is at `AGENTS.md` in the repo root. Read it as authoritative before running, building, or troubleshooting the tool — it covers build, API-key handling, the recommended migration sequence, per-subcommand usage (`analyze`, `flags import`, `targeting import`, `metrics convert`), report analysis, and troubleshooting.

Trigger this rule whenever the user mentions Statsig→LaunchDarkly migration, any of the CLI subcommands, API key setup for `STATSIG_CONSOLE_KEY` / `LD_API_KEY`, the `migration-report.json` / `flag-import-report.json` / `targeting-import-report.json` files, or lossy-targeting opt-ins via `--accept-data-loss`.
