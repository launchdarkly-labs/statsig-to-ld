# statsig-to-ld

A CLI for migrating from Statsig to LaunchDarkly.

> **Important — read this first.** This tool migrates **flag and metric definitions, and targeting rules**, from Statsig to LaunchDarkly. For the SDK code rewrite (Statsig SDK calls → LaunchDarkly SDK calls), use the bundled Claude Code skill at [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md). It does not run experiments. It does not recreate every Statsig feature 1:1. See [`docs/migration-playbook.md`](docs/migration-playbook.md) for what you still need to do yourself.

## Overview

Five CLI subcommands plus a bundled Claude Code skill:

| Surface | What it does | Writes to |
|---|---|---|
| [`analyze`](docs/cli-reference.md#analyze) (CLI) | Read-only Statsig project survey + sizing report | Nothing |
| [`flags import`](docs/cli-reference.md#flags-import) (CLI) | Create LaunchDarkly flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| [`targeting import`](docs/cli-reference.md#targeting-import) (CLI) | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| [`metrics convert`](docs/cli-reference.md#metrics-convert) (CLI) | Convert Statsig metric definitions | LaunchDarkly |
| [`warehouse`](docs/cli-reference.md#warehouse-native-migration) (CLI) | Set up LaunchDarkly warehouse integrations and metric data sources from Statsig (metric definitions are migrated separately by `metrics convert`) | LaunchDarkly + warehouse |
| [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md) (Claude Code skill) | Rewrite Statsig SDK calls → LaunchDarkly SDK calls in your codebase | Your source files |

## Quick start (coding agent)

Paste this into any coding agent

```
Read the README at https://github.com/launchdarkly-labs/statsig-to-ld and follow
the Agent Instructions section.
```

Your agent will then run the [Agent Instructions](#agent-instructions) below — asking which path(s) you need (SDK code, flags, targeting, metrics, warehouse-native integrations), prompting you to export credentials securely in your shell, and running the appropriate surface.

For running the CLI directly without an agent, see [`docs/cli-reference.md`](docs/cli-reference.md) — full subcommand reference, lossy-feature handling, warehouse walkthrough, and the AI-agent integration table.

## Agent Instructions

This section is for AI coding agents (Claude Code, Codex, Cursor, etc.) helping a user migrate from Statsig to LaunchDarkly. Use it to pick the right surface for what the user is migrating, then follow the linked guide. Detailed CLI flags and examples live in [`docs/cli-reference.md`](docs/cli-reference.md).

### Step 1 — Ask what to migrate

Ask the user which paths they need (multi-select). Do this even if you are operating in auto-mode:

| Path | What | Where to go |
|---|---|---|
| **A — SDK code** | Statsig SDK calls → LaunchDarkly SDK calls in application code | Load [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) and run its phases. |
| **B — Flags** | Statsig gates / dynamic configs → LD flag shells | [`statsig-to-ld flags import`](docs/cli-reference.md#flags-import) |
| **C — Targeting rules** | Per-environment rules, rollouts, overrides on existing LD flag shells | [`statsig-to-ld targeting import`](docs/cli-reference.md#targeting-import) (requires B first) |
| **D — Metrics** | Statsig metric definitions → LD metrics | [`statsig-to-ld metrics convert`](docs/cli-reference.md#metrics-convert) |
| **E — Warehouse-native integrations** | Data export + experimentation integrations + LD metric data sources, from a Statsig warehouse-native project | See Step 2 |

### Step 2 — If the user picked E, ask which warehouse scope

Ask one follow-up:

- **"My LaunchDarkly metric data sources already exist — I don't need to create anything."** (Common when the warehouse integration was provisioned for you, set up in the LD UI, or managed via Terraform — this is the **Figma** case.) → **Skip `warehouse` entirely; don't run it at all.** Go straight to path D and tell `metrics convert` which data source to bind to: `--ld-data-source <key>` for a single source, or hand-write a `source-mapping.json` and pass `--source-mapping`. Full detail and the JSON shape are in [Already have LD data sources? Skip `warehouse`](docs/cli-reference.md#already-have-ld-data-sources-skip-warehouse).
- **"I already have the LaunchDarkly warehouse integration set up; I only need to migrate metric data sources."** → [`statsig-to-ld warehouse --only data-sources`](docs/cli-reference.md#running-only-one-phase). Skips the integrations wizard, creates LD data sources, writes `source-mapping.json`.
- **"I haven't set up the LaunchDarkly warehouse integration yet — do all of it."** → [`statsig-to-ld warehouse`](docs/cli-reference.md#warehouse-native-migration) with no `--only` flag. Runs the full pipeline: integrations wizard + data sources + `source-mapping.json`.

**Then run path D** to migrate the warehouse-native metric definitions: `statsig-to-ld metrics convert --source-mapping source-mapping.json` (or `--ld-data-source <key>`) binds each metric to its LD data source. The middle and last paths write `source-mapping.json` for you; the first path (data sources already exist) is where you supply the mapping — or a single `--ld-data-source` key — yourself. The agent shim at [`.claude/agents/statsig-warehouse-migrator.md`](.claude/agents/statsig-warehouse-migrator.md) has the wizard-by-wizard detail for the warehouse subcommand.

> Warehouse-native conversion is the newest, least-tested path. If a metric isn't recognized or converts wrong, run `statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json` (Statsig key only) to export the raw metric definitions, then share that file — redacted — with the LaunchDarkly team. See [Debugging a conversion](docs/cli-reference.md#debugging-a-conversion---dump-raw).

### Step 3 — Check credentials before running anything

**Applies to paths B, C, D, and E only.** Path A handles its own credentials inside the skill (via `ldcli` to `.env`).

Before running any CLI command, check whether the user's shell already has the two required environment variables:

```bash
[ -n "$STATSIG_CONSOLE_KEY" ] && echo "STATSIG_CONSOLE_KEY: set" || echo "STATSIG_CONSOLE_KEY: NOT set"
[ -n "$LD_API_KEY" ]          && echo "LD_API_KEY: set"          || echo "LD_API_KEY: NOT set"
```

If either is `NOT set`, **do not ask the user to paste the key into the chat**. Anything pasted into the chat lands in the conversation transcript and may be logged or persisted. Instead, give the user this exact snippet to run in their own terminal:

```bash
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY   # console-... from Statsig
read -rs LD_API_KEY && export LD_API_KEY                     # api-... from LaunchDarkly
```

Why this form:
- `read -rs` reads with **no echo** (the key never appears on screen) and **no backslash interpretation** (so a `\` in the key isn't misread). This avoids the key landing in shell history that `export KEY=value` would create.
- The two `export` statements then make the values available to any subprocess (including the CLI the agent runs).

**Important constraint:** the user must run these `read -rs && export` lines in **the same shell session that the agent's subprocess inherits from**. If the agent is already running and the user exports keys in a *different* terminal, the agent's subprocess won't see them. After the user reports the export is done, re-check with the snippet above; if values still show `NOT set`, ask the user to restart the agent session so the new shell environment is inherited.

For users on Windows / fish / non-POSIX shells, give the equivalent (`Read-Host -AsSecureString` on PowerShell, `read -s` + `set -x` on fish) — same principle: no echo, no history, no chat-paste.

Only after both env vars are confirmed set should you run any `statsig-to-ld <subcommand>` command. The CLI reads them automatically; never pass keys as `--statsig-key` / `--ld-key` flags when env vars are set, since flag values are visible in `ps` output.

## Prerequisites

- **Go 1.25 or higher** on macOS or Linux — this tool is built from source (see [Installation](#installation))
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API access token** (`api-xxx`) — create at **Account settings → Authorization → Access tokens** with a role that can write flags, metrics, and (optionally) environments in the target project. The Writer role works.
- **For the SDK-rewrite skill only:** Claude Code (or another Claude interface), `node` 18+ to run the skill's helper scripts, and [`ldcli`](https://github.com/launchdarkly/ldcli) (the skill auto-installs it if missing).

## Installation

### CLI (build from source)

Install **Go 1.25 or higher** ([go.dev/dl](https://go.dev/dl/)) on macOS or Linux, then build the binary from the repo root:

```bash
go build -o statsig-to-ld .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-to-ld/cmd.version=1.0.0" \
  -o statsig-to-ld .
```

This produces a `statsig-to-ld` binary in the repo root. Run it from there, passing the subcommand you want:

```bash
./statsig-to-ld --help
./statsig-to-ld metrics convert --help
./statsig-to-ld analyze --ld-project my-project
```

> **Release binaries vs. building from source.** Building from source with `go build` (above) is the recommended path. The tagged release binaries are published mainly for Linux, and for shipping major collections of updates and bug fixes.

### SDK-rewrite skill

The Claude Code skill that rewrites Statsig SDK calls to LaunchDarkly is bundled in this repo at [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md). To make it available to Claude Code in any project (not just this one), symlink or copy it into `~/.claude/skills/`:

```bash
mkdir -p ~/.claude/skills/

# Symlink (recommended — picks up future updates from this repo)
ln -s "$(pwd)/skills/statsig-to-launchdarkly-migrator" \
  ~/.claude/skills/statsig-to-launchdarkly-migrator

# Or, install a snapshot
cp -R skills/statsig-to-launchdarkly-migrator ~/.claude/skills/
```

When running Claude Code inside *this* repository, no install step is needed — the skill auto-loads from `skills/`. A legacy compat shim at [`.claude/agents/statsig-to-launchdarkly-sdk-migrator.md`](.claude/agents/statsig-to-launchdarkly-sdk-migrator.md) redirects users who previously installed the older agent file.

## CLI usage and reference

Full manual flow, per-subcommand reference, lossy-feature handling, the warehouse-native migration walkthrough, EU/FedRAMP, release process, and the AI-agent integration table all live in [`docs/cli-reference.md`](docs/cli-reference.md).

If you arrived here from a search for a specific subcommand or flag, jump to that doc.

## See also

- [`docs/cli-reference.md`](docs/cli-reference.md) — full CLI reference (subcommands, flags, warehouse walkthrough, AI-agent integrations)
- [`docs/migration-playbook.md`](docs/migration-playbook.md) — what this tool **doesn't** do (SDK rewrites, layers, experiments, holdouts, segment recreation, cutover, rollback)
- [`AGENTS.md`](AGENTS.md) — operator guide for AI agents driving the CLI
- [`CHANGELOG.md`](CHANGELOG.md)
- [`CONTRIBUTING.md`](CONTRIBUTING.md)
- [`SECURITY.md`](SECURITY.md)
