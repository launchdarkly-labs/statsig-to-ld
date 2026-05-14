# LLM-as-judge rubric for Statsig → LaunchDarkly migrations

The judge scores migrated code along five dimensions, each on a 1–5 scale.
These are the squishy properties static analysis misses.

A migration passes the judge if every dimension scores ≥ 4.

## Dimensions

### 1. Idiomatic LDContext shape (1–5)
- 5 — `kind` + `key` present, custom fields flattened correctly, `_meta.privateAttributes` is an array of names (not the values), no leftover Statsig nesting (`custom`, `customIDs` as nested objects).
- 3 — required fields present but odd nesting or duplicated data.
- 1 — `LDUser` shape, missing `kind`, `privateAttributes` as object instead of `_meta` array.

### 2. SDK-appropriate naming and methods (1–5)
- 5 — React code uses `useFlags()` with camelCased keys; non-React uses exact names; method names match the SDK's actual surface (`variation`, `boolVariation`, `jsonVariation`, `useFlags`, `useLDClient`).
- 3 — mostly right but inconsistent camelCase or one wrong typed-method choice.
- 1 — hallucinated methods (`getBoolean`, `evaluate`, `useLDFlag`), wrong naming convention for the SDK.

### 3. Fallback completeness and correctness (1–5)
- 5 — every boolean flag falls back to `false`; every JSON flag has a complete fallback object whose keys cover every property the consuming code reads; types match.
- 3 — fallbacks present but some JSON fallbacks are partial.
- 1 — `null` / `undefined` / type-mismatched fallbacks anywhere.

### 4. Observability migration faithfulness (1–5)
- 5 — plugins added ONLY when Statsig source used `@statsig/web-analytics` / `@statsig/session-replay`; `privacyMask` mapped to `privacySetting`; Statsig-only parameters explicitly listed as lost in the report.
- 3 — plugins added correctly but parameter mapping is approximate.
- 1 — plugins added when source had no observability, or `privacyMask` ignored, or Statsig params copy-pasted as if they applied to LD.

### 5. Experiment handling and report quality (1–5)
- 5 — experiments preserved, Statsig SDK kept alongside LD, blocked gates listed in `migration-summary.json`, parallel-SDK behavior clearly explained.
- 3 — experiments preserved but report doesn't list blocked gates.
- 1 — experiments translated to LD code (forbidden), or removed silently.

## Output format the judge must produce

```json
{
  "scores": {
    "ldcontext_shape": 5,
    "sdk_naming_methods": 4,
    "fallback_correctness": 5,
    "observability_faithfulness": 5,
    "experiment_handling": 4
  },
  "min_score": 4,
  "passes": true,
  "comments": {
    "ldcontext_shape": "All contexts have kind + key, _meta correct.",
    "sdk_naming_methods": "React file uses camelCase but server file mixes typed and generic variation methods.",
    "fallback_correctness": "All fallbacks correct.",
    "observability_faithfulness": "SessionReplay added correctly; report lists lost Statsig params.",
    "experiment_handling": "Experiments preserved; could surface blocked-gate list more prominently."
  }
}
```

The runner extracts this JSON and aggregates across fixtures.
