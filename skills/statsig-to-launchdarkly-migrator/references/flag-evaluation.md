# Flag evaluation

Canonical conversion patterns for every Statsig flag-read shape. Always
pair the right LaunchDarkly method with the right fallback type.

## Feature gates (boolean)

```javascript
// Statsig
statsig.checkGate("gate_name")
// LaunchDarkly — JavaScript / Node
client.variation("gate_name", false)
client.boolVariation("gate_name", false)   // preferred when typed methods exist

// LaunchDarkly — React (auto camelCase by default)
const flags = useFlags();
flags.gateName
```

Fallback is **always** `false`. Matches Statsig's "gate not found" behavior.
Never use `null` or `undefined`.

## Dynamic configs (JSON)

```javascript
// Statsig
const cfg = statsig.getConfig("homepage_config");
const title = cfg.get("title", "Default Title");
const limit = cfg.get("limit", 10);

// LaunchDarkly — JavaScript / Node
const cfg = client.jsonVariation("homepage_config", {
  title: "Default Title",
  limit: 10,
});
const title = cfg.title;
const limit = cfg.limit;

// LaunchDarkly — React
const flags = useFlags();
const cfg = flags.homepageConfig ?? { title: "Default Title", limit: 10 };
```

The fallback object MUST include every key the consuming code reads. A
partial fallback silently undefined-references downstream. The static evals
inspect call sites to catch missing fallback keys.

## Typed variation methods (server SDKs)

For Node and other server-side SDKs, prefer typed methods over the generic
`variation`:

| Type | Method |
| --- | --- |
| Boolean | `client.boolVariation(key, context, false)` |
| String | `client.stringVariation(key, context, "default")` |
| Number | `client.numberVariation(key, context, 0)` |
| JSON | `client.jsonVariation(key, context, { ... })` |

Note: server-side SDKs take `context` as the second argument, not implicitly
from `initialize`. Client SDKs hold the context internally.

## Forbidden fallback values

- `null` — breaks downstream null-checks differently from Statsig
- `undefined` — same
- `""` for a boolean gate — type-incoherent
- A non-matching type — e.g. number for a boolean gate

## React flag-name conversion

React auto-camelCases flag keys (`admin_panel_access` → `adminPanelAccess`).
Migration must produce the camelCased key in React code AND record both
forms in `migration-summary.json`. To opt out, configure
`reactOptions: { useCamelCaseFlagKeys: false }` on the provider.

Non-React SDKs use the exact flag name. Mixing the conventions across a
codebase is a footgun the migration summary should flag.

## Method-name pitfalls

Common stale or wrong names that show up in agentic migrations:

| Wrong | Right |
| --- | --- |
| `client.getBoolean(...)` | `client.variation(key, false)` or `client.boolVariation(...)` |
| `client.evaluate(...)` | `client.variation(...)` |
| `client.getConfig(...)` (LD) | `client.jsonVariation(...)` |
| `useLDFlag(...)` | `useFlags().flagName` |

The static evals lint for the wrong names.
