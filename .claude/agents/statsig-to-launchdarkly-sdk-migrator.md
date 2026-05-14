---
name: statsig-to-launchdarkly-sdk-migrator
description: Legacy compatibility shim for the Statsig→LaunchDarkly migration skill. The canonical implementation is now a Claude Code skill at `skills/statsig-to-launchdarkly-migrator/SKILL.md` — load that for migrations. This file exists only so existing installs at `~/.claude/agents/statsig-to-launchdarkly-sdk-migrator.md` keep resolving. Use the skill when migrating JavaScript/TypeScript/React/Node.js code from Statsig to LaunchDarkly. The skill handles latest-SDK-version resolution via live npm lookup, the `ldcli` + `.env` key flow, and ships with static + LLM-as-judge evals. CRITICAL: agentic coding tools routinely import a stale LaunchDarkly Node SDK pulled from training data; the skill always resolves the current version via live npm lookup before writing any code.
model: sonnet
color: purple
---

# Statsig → LaunchDarkly migration (legacy compat shim)

The canonical implementation is now a Claude Code skill at:

```
skills/statsig-to-launchdarkly-migrator/SKILL.md
```

This file exists only as a compatibility shim for installs that still resolve
the old agent path (`~/.claude/agents/statsig-to-launchdarkly-sdk-migrator.md`).
New installs should use the skill path instead. The skill adds, over the previous
agent definition:

1. **Live SDK-version resolution** — `scripts/resolve-versions.mjs` runs `npm view` for every LaunchDarkly package before writing any `import` or `package.json` edit. Defeats the stale-training-data Node SDK problem.
2. **`ldcli` + `.env` SDK-key flow** — `scripts/install-ldcli.mjs` + `scripts/write-env.mjs` install ldcli, accept the key on stdin (never via Claude), and refuse to write if `.env` is not gitignored.
3. **Half-implement-then-test rollout** — explicit test-env-first sequence with `ldcli flags get` cross-checks before flipping prod.
4. **Static eval harness** — 9 assertions over migrated source + a `package.json` version check. Self-tested: `node skills/statsig-to-launchdarkly-migrator/evals/static/self-test.mjs`.
5. **LLM-as-judge eval harness** — five-dimension rubric for the squishy cross-file properties static analysis can't catch.

## How to use

When invoked, follow `skills/statsig-to-launchdarkly-migrator/SKILL.md` end-to-end — do NOT skip Phase 1 (version resolution) or Phase 2 (`ldcli` key flow). The phases are ordered for a reason; skipping Phase 1 is the #1 way agentic LaunchDarkly migrations fail.

## Hard rules (lifted from the skill)

- Never reuse a Statsig SDK key
- Never write a literal Client-Side ID in code — always `process.env.*`
- Never use `null` or `undefined` as a flag fallback
- Never emit `launchdarkly-node-server-sdk` (legacy unscoped name) — the current package is `@launchdarkly/node-server-sdk`
- Never migrate experiments
- Never skip live version resolution
