# Observability and session replay migration

Only migrate observability if the codebase actually imports one of:

- `@statsig/web-analytics` — autocapture
- `@statsig/session-replay` — session replay

If neither is present, do NOT add LaunchDarkly observability plugins. The
init call stays lean.

## Imports

```javascript
// Statsig
import { StatsigAutocapture } from '@statsig/web-analytics';
import { StatsigSessionReplay } from '@statsig/session-replay';

// LaunchDarkly
import Observability, { LDObserve } from '@launchdarkly/observability';
import SessionReplay, { LDRecord } from '@launchdarkly/session-replay';
```

Both `@launchdarkly/observability` and `@launchdarkly/session-replay` are
plugins to the JS client SDK 3.7.0+. The version-resolver script pins the
current published versions.

## Adding plugins to `initialize`

```javascript
const client = initialize(process.env.LD_CLIENT_SIDE_ID, context, {
  plugins: [
    new Observability({
      tracingOrigins: true,
      networkRecording: {
        enabled: true,
        recordHeadersAndBody: true,
      },
    }),
    new SessionReplay({
      privacySetting: 'default', // 'none' | 'default' | 'strict'
    }),
  ],
});
```

## Session-replay parameter mapping

Statsig and LaunchDarkly session replay have meaningfully different
parameter surfaces. Map what you can; surface lost features in the report.

| Statsig | LaunchDarkly | Notes |
| --- | --- | --- |
| `privacyMask: true` | `privacySetting: 'default'` or `'strict'` | Pick `'strict'` if compliance-driven |
| `privacyMask: false` | `privacySetting: 'none'` | |
| `maxSessionDurationMs` | — | No LD equivalent; record as lost |
| `recordConsoleErrors` | — | No LD equivalent; record as lost |
| (any other Statsig-specific param) | — | Record as lost |

The `migration-summary.json` MUST include the parameter delta under
`observability_migration.session_replay.lost_features`.

## Autocapture

LaunchDarkly's `Observability` plugin covers the same surface as Statsig's
web-analytics autocapture. Network recording is opt-in via
`networkRecording: { enabled: true }`. If the Statsig setup used
non-default options, scan for them and translate; default options on both
sides line up well enough that a vanilla migration is usually correct.

## When the customer wants to opt OUT of session replay during migration

A common ask: "we want to migrate to LaunchDarkly but turn session replay
on later, after we audit the privacy posture." Pattern:

1. Add the `SessionReplay` plugin to the init call but set
   `privacySetting: 'strict'` and gate its `LDRecord.start()` behind a
   LaunchDarkly flag.
2. Default the flag off in all environments.
3. Flip on per-environment after privacy review.

Document this gating in the migration summary's `next_steps`.
