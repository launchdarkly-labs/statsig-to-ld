# statsig-to-ld

End-to-end migration from Statsig to LaunchDarkly: SDK code rewrites + flag, targeting, and metric definitions.

> **Important — read this first.** This repo ships **two surfaces**: a Go CLI (`statsig-to-ld`) that migrates flag, targeting, and metric *definitions* via the Statsig and LaunchDarkly REST APIs, and a Claude Code skill (`skills/statsig-to-launchdarkly-migrator/`) that rewrites Statsig SDK calls in your application code. Neither recreates experiments. See [`docs/migration-playbook.md`](docs/migration-playbook.md) for what neither tool does.

## Overview

Four CLI subcommands plus a bundled Claude Code skill:

| Surface | What it does | Writes to |
|---|---|---|
| [`analyze`](#analyze) (CLI) | Read-only Statsig project survey + sizing report | Nothing |
| [`flags import`](#flags-import) (CLI) | Create LaunchDarkly flag shells from Statsig gates and dynamic configs | LaunchDarkly |
| [`targeting import`](#targeting-import) (CLI) | Apply per-environment targeting rules, rollouts, and overrides | LaunchDarkly |
| [`metrics convert`](#metrics-convert) (CLI) | Convert Statsig metric definitions | LaunchDarkly |
| [`skills/statsig-to-launchdarkly-migrator/`](skills/statsig-to-launchdarkly-migrator/SKILL.md) (Claude Code skill) | Rewrite Statsig SDK calls → LaunchDarkly SDK calls in your codebase | Your source files |

## Quick start (Claude Code)

Paste this into Claude Code (or any Claude interface with this repo on the filesystem):

```
Read the README at https://github.com/launchdarkly-labs/statsig-to-ld and follow the Agent Instructions section to help me migrate from Statsig to LaunchDarkly.
```

Claude will then run the [Agent Instructions](#agent-instructions) below — asking which paths you need, prompting you to export credentials in your shell, and running the appropriate surface for each path.

For users running the CLI directly without an agent, see [Manual quick start](#manual-quick-start).

## Agent Instructions

When invoked via the prompt above, follow these steps precisely.

### Step 1 — Scope the migration (optional but recommended)

Before asking the user what to do, propose running `statsig-to-ld analyze` as a read-only sizing pass. It surfaces:

- How many gates, dynamic configs, environments, and metrics they have
- Which targeting features will be [lossy](#lossy-targeting-features) on import
- Which metric types are unsupported

This requires only a Statsig Console API key. If the user agrees, follow [Step 3a (credential export)](#step-3a-credential-export) for `STATSIG_CONSOLE_KEY`, then run:

```bash
statsig-to-ld analyze --statsig-key console-...
```

Skip this step if the user has already analyzed, or wants to dive straight in.

### Step 2 — Ask the user what to migrate

Ask which of the following the user wants. They may select one or more:

- **A. Migrate SDK code** — rewrite Statsig SDK calls in their codebase to LaunchDarkly SDK calls. Uses the Claude Code skill at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md).
- **B. Create feature flags in LaunchDarkly** — create LD flag shells from their Statsig gates and dynamic configs. Uses `statsig-to-ld flags import`.
- **D. Migrate targeting rules** — apply per-environment targeting/rollouts/overrides from Statsig to the flag shells from B. Uses `statsig-to-ld targeting import`.
- **C. Migrate metrics** — convert their Statsig metric definitions to LD metrics. Uses `statsig-to-ld metrics convert`.

If the user selects multiple paths, execute them in order **A → B → D → C**:

1. **A first** because the SDK migration emits `migration-summary.json` with the canonical flag keys that B should match.
2. **B before D** because targeting rules apply to flag shells that must already exist.
3. **C last** because metrics are independent of the flag work and are cheaper to iterate on after the rest is in motion.

The lettering matches the historical bootstrap (A/B/C from earlier versions); D is the targeting addition.

### Step 3 — Run each selected path

#### Step 3a — Credential export

Both the skill (Path A) and the CLI (Paths B/D/C) need credentials. **Never ask the user to paste keys into the chat** — that would expose them in your context window. Instead, instruct the user to run these in their terminal, in the same shell session they'll use for this migration:

```bash
# For Path A (SDK code rewrite via skill) — done interactively by the skill via `ldcli`.
# The skill's Phase 2 writes LD_CLIENT_SIDE_ID (or LD_SDK_KEY) to .env without it
# passing through the chat. See skills/statsig-to-launchdarkly-migrator/references/sdk-key-setup.md.

# For Paths B/D/C (CLI subcommands):
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY   # console-... from Statsig
read -rs LD_API_KEY && export LD_API_KEY                     # api-... from LaunchDarkly
```

Wait for the user to confirm before proceeding. If a CLI command runs without a key in its env, it prompts interactively with echo disabled — that's also acceptable, but flag exports avoid the per-command prompt.

#### Step 3b — Path A: SDK code (skill)

Load the skill at [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) and run its seven phases in order. The skill:

- Scans the codebase for Statsig SDK calls
- Resolves the current LaunchDarkly SDK versions via live `npm view` (Phase 1) — never trust training-data versions
- Pulls the LD Client-Side ID via `ldcli` and writes it to `.env`
- Translates imports, initialization, contexts, flag evaluations, observability
- Flags experiments (`getExperiment`, `useExperiment`, `useLayer`) as blocked — both SDKs run in parallel
- Outputs `migration-summary.json` with the canonical flag-key list for Path B

#### Step 3c — Path B: flags import (CLI)

```bash
# Dry-run first — confirm what would be created
statsig-to-ld flags import --all --ld-project <ld-project-key> --dry-run

# Apply (creates flag shells, off in all envs)
statsig-to-ld flags import --all --ld-project <ld-project-key>
```

Confirm the LD project key with the user before running. Dedupe is by sanitized LD key, so re-runs are idempotent. See [`flags import`](#flags-import) for filters and the full flag list.

#### Step 3d — Path D: targeting import (CLI)

Requires flag shells to exist from Path B.

```bash
# Strict (default): skip flags whose targeting would be lossy
statsig-to-ld targeting import --all --ld-project <ld-project-key> --dry-run
statsig-to-ld targeting import --all --ld-project <ld-project-key>
```

Review the dry-run output and confirm any [lossy features](#lossy-targeting-features) the user wants to accept via `--accept-data-loss=...`.

#### Step 3e — Path C: metrics convert (CLI)

```bash
# Dry-run first
statsig-to-ld metrics convert --all --ld-project <ld-project-key> --dry-run

# Apply
statsig-to-ld metrics convert --all --ld-project <ld-project-key>
```

If the user has unit types beyond `userID` (e.g. `companyID`), prompt them to provide a unit-type mapping; see [Custom unit types](#custom-unit-types-company-level-experiments).

### Step 4 — Summarize results

After all selected paths complete, present a clear summary:

- **Path A**: files changed, flag keys in `migration-summary.json`, items blocked by experiments
- **Path B**: flags created, flags skipped (already existed), incompatible flags
- **Path D**: targeting applied, flags skipped as lossy, approximated operators
- **Path C**: metrics converted, metrics skipped, warnings (data loss, lost Statsig features)
- **Next steps**: experiment migration (manual in LD), parallel SDK testing checklist, segments to recreate in LD UI, [`docs/migration-playbook.md`](docs/migration-playbook.md) for the rest

## Credentials reference

| Path | Required | How to provide |
|---|---|---|
| Path A — SDK code | `LD_CLIENT_SIDE_ID` (client SDKs) or `LD_SDK_KEY` (Node SDK) | Inserted into `.env` interactively by the skill via `ldcli`; never via chat |
| Paths B / D / C — CLI | `LD_API_KEY` + `STATSIG_CONSOLE_KEY` | Shell export (`read -rs`), or `--ld-key` / `--statsig-key` flags, or interactive prompt |

Where to find each key:

- **`LD_API_KEY`** — LaunchDarkly → Account settings → Authorization → Access tokens. Needs the **Writer** role (or finer-grained perms covering flags, metrics, environments) in the target project.
- **`LD_CLIENT_SIDE_ID` / `LD_SDK_KEY`** — LaunchDarkly → Account settings → Projects → [project] → [environment]. Use Client-Side ID for browser/React; Server SDK key for Node.
- **`STATSIG_CONSOLE_KEY`** — Statsig Console → Project Settings → Keys & Environments. The `console-...` Console API key, not the SDK keys.

Paths B / D / C share `LD_API_KEY` + `STATSIG_CONSOLE_KEY` — export once and all three CLI paths work.

## Prerequisites

- Go 1.24+ (to build from source) or a pre-built binary from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page
- A Statsig **Console API Key** (`console-xxx`) — create at Statsig Console > Project Settings > Keys & Environments
- A LaunchDarkly **API access token** (`api-xxx`) — create at **Account settings → Authorization → Access tokens** with a role that can write flags, metrics, and (optionally) environments in the target project. The Writer role works.
- For Path A only: Claude Code (or compatible Claude interface) + `node` 18+ for the skill's helper scripts + [`ldcli`](https://github.com/launchdarkly/ldcli) (the skill installs it if missing).

## Installation

### CLI (from source)

```bash
go build -o statsig-to-ld .

# With version stamped into the binary
go build -ldflags "-X github.com/launchdarkly-labs/statsig-to-ld/cmd.version=1.0.0" \
  -o statsig-to-ld .
```

### CLI (pre-built binary)

Download from the [Releases](https://github.com/launchdarkly-labs/statsig-to-ld/releases) page.

### Skill (for Path A — SDK code rewrite)

The skill is bundled in this repo at `skills/statsig-to-launchdarkly-migrator/`. To make it available to Claude Code in any project, symlink or copy it into `~/.claude/skills/`:

```bash
mkdir -p ~/.claude/skills/
ln -s "$(pwd)/skills/statsig-to-launchdarkly-migrator" ~/.claude/skills/statsig-to-launchdarkly-migrator
# or, to install a snapshot:
cp -R skills/statsig-to-launchdarkly-migrator ~/.claude/skills/
```

If you previously curl-installed the legacy agent file from `statsig-to-ld-agent`, it now points at the skill — no action needed on your side.

## Manual quick start

For users running the CLI directly without an agent. (For agent-driven, see [Quick start (Claude Code)](#quick-start-claude-code).)

```bash
# 1. Set API keys (recommended — avoids shell history exposure)
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY

# 2. Scope the migration: how much work, what won't import faithfully
statsig-to-ld analyze --ld-project my-project

# 3. Create flag shells — off in all envs, no production impact
statsig-to-ld flags import --all --ld-project my-project

# 4. Preview targeting — fail-closed by default
statsig-to-ld targeting import --all --ld-project my-project --dry-run

# 5. Apply targeting (review the dry-run report first)
statsig-to-ld targeting import --all --ld-project my-project

# 6. Convert metrics — independent of the flag work
statsig-to-ld metrics convert --all --ld-project my-project

# 7. For SDK code rewrites, launch Claude Code in your project and load
#    skills/statsig-to-launchdarkly-migrator/SKILL.md. Or read the migration
#    playbook to do it by hand.
cat docs/migration-playbook.md
```

## API key security

API keys can be provided three ways (precedence order):

1. **Command-line flags** (`--statsig-key`, `--ld-key`) — visible in shell history and `ps` output. Use only in CI/CD where keys come from a secrets manager.
2. **Environment variables** (`STATSIG_CONSOLE_KEY`, `LD_API_KEY`) — not in `ps`, but `export KEY=value` lands in history. Use the `read -rs` form below.
3. **Interactive prompt** — **most secure** for interactive use. If a key isn't provided via flag or env, the tool prompts with echo disabled.

```bash
# Set without history exposure
read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
read -rs LD_API_KEY && export LD_API_KEY
```

## Subcommands

### analyze

Read-only sizing report. Surveys gates, dynamic configs, environments, and metrics and tells you what will import faithfully, what will be [lossy](#lossy-targeting-features), and what will be skipped.

```bash
# Statsig-only analysis — no LD account needed yet
statsig-to-ld analyze --statsig-key console-...

# Full analysis including env-mapping preview
statsig-to-ld analyze --ld-project my-project

# Save the structured report alongside the table
statsig-to-ld analyze --ld-project my-project --output analyze.json
```

See `statsig-to-ld analyze --help` for the full flag list.

### flags import

Creates LaunchDarkly **flag shells** from Statsig feature gates and dynamic configs. Variations, default values, tags, and the maintainer are set; per-environment targeting is **not** — run [`targeting import`](#targeting-import) afterwards.

```bash
# Dry-run first
statsig-to-ld flags import --all --ld-project my-project --dry-run

# Import everything (gates + dynamic configs)
statsig-to-ld flags import --all --ld-project my-project

# Gates only, filtered by a Statsig tag
statsig-to-ld flags import --all --import-type gates --include-tag p0 \
  --ld-project my-project
```

**Idempotency**: dedupe is by sanitized LD key, not display name. Renaming a Statsig gate between runs does **not** create a duplicate.

See `statsig-to-ld flags import --help` for the full flag list.

### targeting import

Applies per-environment targeting (rules, rollouts, user/context targets) to flag shells previously created by `flags import`. Reconciles Statsig environments to LaunchDarkly environments by case-insensitive name; auto-creates missing LD envs (turn off with `--no-create-envs`).

```bash
# Strict (default): skip flags whose targeting would be lossy
statsig-to-ld targeting import --all --ld-project my-project

# Permissive: accept all lossy features (matches the predecessor lambda)
statsig-to-ld targeting import --all --accept-data-loss=all \
  --ld-project my-project

# Surgical: accept specific lossy features
statsig-to-ld targeting import --all \
  --accept-data-loss=segments,unreachable_rules \
  --ld-project my-project
```

> **Re-run caveat (v1.0)**: `targeting import` overwrites the per-env settings on every matching flag. If you hand-tune flag targeting in the LaunchDarkly UI after import, re-running this command will overwrite those edits. A future release will add a `--diff` preview default for re-runs ([roadmap](#planned-follow-ups)).

See `statsig-to-ld targeting import --help` for the full flag list.

### metrics convert

Converts Statsig metric definitions into LaunchDarkly metrics. Supports both Statsig Cloud and Warehouse Native metrics, with idempotent re-runs, parallel processing, and structured migration reports.

```bash
# Dry-run preview
statsig-to-ld metrics convert --all --dry-run

# Bulk convert
statsig-to-ld metrics convert --all --ld-project my-project

# Single metric
statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

# Warehouse Native with a single data source
statsig-to-ld metrics convert --all --ld-project my-project \
  --ld-data-source snowflake-ds

# Warehouse Native with per-source mapping
statsig-to-ld metrics convert --all --ld-project my-project \
  --source-mapping sources.json
```

Where `sources.json` is:

```json
{"purchases_table": "snowflake-purchases-ds", "sessions_table": "snowflake-sessions-ds"}
```

#### Custom unit types (company-level experiments)

If your Statsig project uses unit types beyond `userID` (e.g. `companyID`, `teamID`), map them to your LD context kinds:

```bash
statsig-to-ld metrics convert --all --ld-project my-project \
  --unit-type-mapping unit-types.json
```

```json
{"companyID": "company", "teamID": "team"}
```

Without this mapping, non-`userID` unit types are lowercased (e.g. `companyID` → `companyid`) and a warning is emitted.

#### Metric type conversion

| Statsig type | LD kind | Status |
|---|---|---|
| `event_count_custom` | custom | Supported |
| `sum` | custom (isNumeric) | Supported |
| `mean` | custom (isNumeric, average) | Supported |
| `event_user` | custom | Supported |
| `event_user_window` | custom | Supported |
| `ratio` | — | Not yet supported in LD |
| `funnel` | — | Requires LD metric group |
| `composite` | — | Not supported in LD |
| `percentile` | — | Not supported as LD type |

See `statsig-to-ld metrics convert --help` for the full flag list.

## Lossy targeting features

`targeting import` is **fail-closed by default**: flags whose Statsig source uses any of the features below are skipped (with a `skipped_lossy` entry in the report). To import them anyway, opt in via `--accept-data-loss`:

```bash
# Accept all lossy features
--accept-data-loss=all

# Accept only specific features
--accept-data-loss=segments,prerequisites
```

| Feature | Why it's lossy |
|---|---|
| `passes_segment` / `fails_segment` | Statsig segments aren't auto-recreated in LD; the condition is dropped. See [migration playbook §2](docs/migration-playbook.md#2-statsig-segments). |
| `passes_gate` / `fails_gate` | Gate prerequisites aren't auto-recreated; the condition is dropped. Set up LD flag prerequisites manually. |
| Custom `unit_id` (non-`userID`) | Targeting is squashed to LD's `user` context kind in v1; per-unit targeting fidelity is lost. |
| Multi-variant DC overrides | Statsig's override API is binary pass/fail per user; multi-variant fidelity is lost. |
| Unreachable trailing rules | Rules after a "public" (match-everyone) rule are dropped — they're unreachable in Statsig's first-match-wins model too. |

There's also a softer category — **approximated operators** — that import with a warning but do not fail-closed:

| Operator | Approximation |
|---|---|
| `version_gte` | Emitted as `semVerGreaterThan` (LD has no `semVerGreaterThanOrEqual`) |
| `version_lte` | Emitted as `semVerLessThan` |

## Statsig features not carried over (metrics convert)

| Feature | Warning | Impact |
|---|---|---|
| Event filter criteria | `DATA LOSS` | LD metric matches all events, not just the filtered subset. Manual filter setup required. |
| Winsorization | Outlier clipping not applied | Experiment results may be more sensitive to outliers |
| Per-unit capping | Daily cap not applied | No per-user-per-day value cap |
| Log transform | Values not log-transformed | Distribution shape may differ |
| Custom rollup windows | Measurement windows not applied | LD uses full experiment duration |
| Daily participation rate | Uses standard binary conversion | Different aggregation method |
| Count distinct | Counts all occurrences instead | Higher counts than Statsig |
| Metadata aggregation | Aggregates `track()` metricValue | Ensure events send correct value |

Metrics with these features are still converted; the warning appears per-entry in the report.

## EU / FedRAMP instances

```bash
statsig-to-ld <subcommand> ... --ld-url https://app.eu.launchdarkly.com
```

All subcommands accept `--ld-url` and `--statsig-url` overrides.

## Planned follow-ups

These are explicit non-goals for v1.0; they're tracked for follow-up releases:

- **`segments export`** — dump Statsig segment definitions to JSON so users can recreate them in LD by hand, via Terraform, or via `ldcli`.
- **`targeting import --update-existing --diff`** — preview-before-overwrite for re-running targeting on already-targeted flags.
- **Split.io and Unleash sources** — out of scope for v1; the codebase has hooks but no logic.

## Releasing a new version (contributors)

Releases are driven by Git tags. Pushing a `v*` tag triggers CI to cross-compile binaries for macOS, Linux, and Windows and publish them to the [Releases page](https://github.com/launchdarkly-labs/statsig-to-ld/releases).

```bash
git tag v0.2.0
git push origin v0.2.0
```

Release notes are generated from commits since the previous tag.

## See also

- [`skills/statsig-to-launchdarkly-migrator/SKILL.md`](skills/statsig-to-launchdarkly-migrator/SKILL.md) — the canonical Claude Code skill for Path A (SDK code rewrites)
- [`docs/migration-playbook.md`](docs/migration-playbook.md) — what neither tool does (layers, experiments, holdouts, segment recreation, cutover, rollback)
- [`CHANGELOG.md`](CHANGELOG.md)
- [`CONTRIBUTING.md`](CONTRIBUTING.md)
- [`SECURITY.md`](SECURITY.md)
