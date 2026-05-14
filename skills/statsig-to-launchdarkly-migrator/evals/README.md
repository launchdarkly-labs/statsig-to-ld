# Evals

Two eval harnesses cover the skill at different fidelity / cost points.

## Static evals (fast, deterministic, CI-able)

```bash
# Lint the bundled "expected" fixtures (sanity check the harness itself)
node skills/statsig-to-launchdarkly-migrator/evals/static/run.mjs

# Lint a real migration output
node skills/statsig-to-launchdarkly-migrator/evals/static/run.mjs --input ./

# Meta-test: assertion library is wired correctly
node skills/statsig-to-launchdarkly-migrator/evals/static/self-test.mjs
```

The static evals check rules that have a definite right answer:

- No legacy LaunchDarkly package names (`launchdarkly-node-server-sdk`, `ldclient-node`)
- No leftover Statsig SDK imports unless the file uses an experiment API
- No literal Statsig keys (`client-...`) anywhere in migrated code
- No hardcoded LaunchDarkly Client-Side IDs / SDK Keys (must read from `process.env`)
- `initialize()` reads the key from `process.env.LD_CLIENT_SIDE_ID` or `LD_SDK_KEY`
- Variation fallbacks are correct types and never `null`/`undefined`
- LDContext literals have both `kind` and `key`
- No `LDUser` type (deprecated pre-context API)
- No hallucinated method names (`client.getBoolean`, `client.evaluate`, `useLDFlag`, etc.)
- `package.json` declares the current major versions for every LaunchDarkly package

The `package.json` check reads `migration-versions.json` (produced by
`scripts/resolve-versions.mjs`) when present, so it stays current as the
LaunchDarkly SDKs evolve.

### Adding a static assertion

1. Add a function to `evals/static/assertions.mjs` returning `{ ok, name, detail }`.
2. Export it in `SOURCE_ASSERTIONS` (or wire it into the package.json path).
3. Add a fixture under `evals/static/fixtures/bad/` that the new assertion catches.
4. Run `node evals/static/self-test.mjs` to confirm.

## LLM-as-judge evals (slower, costs Anthropic API tokens)

```bash
# Score the bundled "expected" fixtures
node skills/statsig-to-launchdarkly-migrator/evals/judge/run.mjs

# Score a real migration output
node skills/statsig-to-launchdarkly-migrator/evals/judge/run.mjs --input ./ --out judge-results.json
```

Requires the `claude` CLI on PATH. Each fixture is one non-interactive
`claude -p` call.

Scores five dimensions on a 1–5 scale; a fixture passes if every dimension
scores ≥ 4. Rubric lives in `evals/judge/rubric.md`.

Dimensions:

1. Idiomatic LDContext shape
2. SDK-appropriate naming and methods
3. Fallback completeness and correctness
4. Observability migration faithfulness
5. Experiment handling and report quality

### When to run the judge

- Before tagging a skill release
- After non-trivial edits to the canonical migration patterns
- Not on every commit — too slow and costs API tokens

### When NOT to use the judge

If a property is checkable with a regex or AST walk, write a static
assertion instead. The judge is for the squishy cross-file properties.
