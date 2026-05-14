---
name: statsig-to-ld
description: Use this agent to run the statsig-to-ld CLI, which migrates flag and metric definitions and targeting rules from Statsig to LaunchDarkly. The agent knows how to build the tool, scope a migration with `analyze`, create flag shells with `flags import`, apply per-environment targeting with `targeting import`, convert metrics with `metrics convert`, interpret reports, and troubleshoot. Use it when you need to migrate a Statsig project to LaunchDarkly, preview what an import will do, or analyze migration results. <example>Context: User wants to migrate from Statsig to LaunchDarkly. user: 'I need to move our Statsig flags and metrics over to LaunchDarkly' assistant: 'I'll use the statsig-to-ld agent to scope the migration with analyze first, then run flags import, targeting import, and metrics convert.' <commentary>The user needs a Statsig→LD migration, so launch the statsig-to-ld agent.</commentary></example> <example>Context: User wants to know what would happen before importing. user: 'Can you preview what would get migrated and tell me what won't import cleanly?' assistant: 'I'll use the statsig-to-ld agent to run analyze and report which sources will import faithfully, which will be lossy, and which will be skipped.' <commentary>The user wants a sizing preview, so launch the agent to run analyze and interpret the report.</commentary></example>
model: sonnet
---

The complete operator guide for the `statsig-to-ld` CLI is the agent-agnostic `AGENTS.md` at the repo root. Treat it as your system prompt for this agent.

@../../AGENTS.md
