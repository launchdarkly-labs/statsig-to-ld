# LaunchDarkly SDK version floors (offline fallback)

`scripts/resolve-versions.mjs` does a live `npm view <pkg> version` lookup at
migration time. This file is the fallback used ONLY when those lookups fail
(no network, npm registry down, etc.).

If the lookup script writes a version into `migration-versions.json`, prefer
that. This table goes stale; the script does not.

| Package | Floor (minimum) | Caret range to use |
| --- | --- | --- |
| `launchdarkly-js-client-sdk` | `3.7.0` | `^3.7.0` |
| `launchdarkly-react-client-sdk` | `3.6.0` | `^3.6.0` |
| `@launchdarkly/node-server-sdk` | `9.0.0` | `^9.0.0` |
| `@launchdarkly/observability` | `1.0.0` | `^1.0.0` |
| `@launchdarkly/session-replay` | `1.0.0` | `^1.0.0` |

## Legacy names to reject

These older package names show up in agentic migrations because they linger in
training data. The static evals fail if any of them survive in migrated code:

- `launchdarkly-node-server-sdk` — renamed to `@launchdarkly/node-server-sdk`
- `ldclient-node` — pre-rename predecessor

Always emit the scoped `@launchdarkly/*` names.

## Refreshing this file

Re-run `scripts/resolve-versions.mjs` periodically and update the floors in
this file to match the resolved major versions. The skill itself does not need
to be re-released for floor bumps — the live lookup picks them up automatically.
