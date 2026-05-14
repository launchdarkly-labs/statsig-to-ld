# StatsigUser → LDContext

LaunchDarkly contexts have a different shape than Statsig users. Every
context MUST have `kind` and `key` — those two are required, everything
else is optional.

## Full field mapping

```javascript
// Statsig
const statsigUser = {
  userID: "user-123",         // → key (REQUIRED)
  email: "anna@example.com",
  ip: "192.168.1.1",          // typically auto-inferred in LD; drop unless explicit
  country: "US",              // typically auto-inferred from IP
  locale: "en-US",
  appVersion: "1.2.3",
  systemName: "iOS",
  systemVersion: "15.0",
  browserName: "Chrome",
  browserVersion: "96.0",
  userAgent: "Mozilla/5.0",   // auto-captured by LD in browser
  custom: {                   // FLATTENED to top-level in LD
    organization: "Global Health",
    jobFunction: "doctor",
    tier: "premium",
  },
  customIDs: {                // FLATTENED to top-level in LD; behavior differs
    orgID: "org-456",
    teamID: "team-789",
  },
  privateAttributes: {        // moved into _meta.privateAttributes (array of names)
    salary: 100000,
    ssn: "xxx-xx-xxxx",
  },
};

// LaunchDarkly
const ldContext = {
  kind: "user",                 // REQUIRED
  key: "user-123",              // REQUIRED (from userID)
  email: "anna@example.com",
  country: "US",
  locale: "en-US",
  appVersion: "1.2.3",
  systemName: "iOS",
  systemVersion: "15.0",
  browserName: "Chrome",
  browserVersion: "96.0",

  // custom flattened
  organization: "Global Health",
  jobFunction: "doctor",
  tier: "premium",

  // customIDs flattened (note: may affect bucketing differently than Statsig)
  orgID: "org-456",
  teamID: "team-789",

  // privateAttributes moved to _meta
  salary: 100000,
  ssn: "xxx-xx-xxxx",
  _meta: {
    privateAttributes: ["salary", "ssn"],
  },
};
```

## Multi-context

When the app reasons about more than one entity (user + org + device),
LaunchDarkly supports `kind: "multi"`:

```javascript
const multiContext = {
  kind: "multi",
  user: {
    key: "user-123",
    email: "anna@example.com",
  },
  organization: {
    key: "org-456",
    name: "Global Health",
    plan: "enterprise",
  },
  device: {
    key: "device-abc",
    type: "mobile",
    os: "iOS",
  },
};
```

Statsig's `customIDs` is the rough analog — but `customIDs` in Statsig
affect experiment bucketing in ways `kind: "multi"` does not exactly
reproduce. Surface this in the migration summary so reviewers know to
re-validate any targeting that depended on `customIDs`.

## Key (not optional) gotchas

- Statsig's `userID` is required in some SDKs but optional in others.
  LaunchDarkly's `key` is always required. If the source code constructs a
  Statsig user without `userID`, the migration MUST produce a `key`
  somehow — generate a stable per-session key, document the choice, and
  flag it in the summary.
- Don't put PII into `key`. The key is logged in analytics events.
- Don't change a key for the same logical user across sessions — it breaks
  consistent bucketing.

## What gets auto-captured by LD

The browser SDK auto-captures user agent and (depending on configuration)
infers country from IP. Don't duplicate those fields in the context unless
the source had an explicit value the user wanted to preserve.

## TypeScript

```typescript
import type { LDContext } from 'launchdarkly-js-client-sdk';

const context: LDContext = {
  kind: 'user',
  key: 'user-123',
  // ...
};
```

The type is `LDContext`, not `LDUser`. The pre-context `LDUser` API is
deprecated and the static evals reject it.
