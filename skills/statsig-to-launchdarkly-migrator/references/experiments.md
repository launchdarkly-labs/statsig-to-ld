# Experiments are not migrated

Statsig experiments (`getExperiment`, `useExperiment`, `useLayer`) have no
1:1 LaunchDarkly equivalent that can be auto-migrated. They are preserved
as-is and the affected feature gates are blocked.

## Detection

Any of these calls trip the experiment gate:

- `statsig.getExperiment("name")`
- `useExperiment("name")` (React)
- `useLayer("name")` (React)
- Reading a parameter off an experiment object (`.get("param", default)`)

## Behavior when experiments are found

1. Keep the `statsig-js` / `@statsig/react-bindings` imports in place.
2. Add the LaunchDarkly imports alongside — both SDKs run in parallel.
3. For each experiment, list every feature gate name that reads off the
   same experiment object. Those gates are **blocked** — do not migrate
   them. They keep using Statsig.
4. Surface a prominent warning in the output:

```
⚠️ EXPERIMENT DETECTED: cannot fully migrate
- Found experiment: "checkout_flow_test"
- Blocked feature gates (still using Statsig):
  - "new_checkout_enabled"
  - "express_checkout_button"
- These cannot be migrated until experiments are manually recreated in
  LaunchDarkly.
```

5. Record the experiments and blocked gates in `migration-summary.json`
   under `not_migrated.experiments` and `not_migrated.blocked_gates`.

## Parallel SDK operation

When experiments survive, the migrated code looks like:

```javascript
import statsig from 'statsig-js';                                // KEPT for experiments
import { initialize } from 'launchdarkly-js-client-sdk';         // ADDED for migrated flags

// Experiment paths still use Statsig
if (statsig.getExperiment("checkout_test").get("variant") === "B") { ... }

// Plain feature-gate paths use LaunchDarkly
if (ldClient.variation("new_dashboard", false)) { ... }
```

This is expected and correct during the transition. Tell the user the
Statsig SDK comes out only after experiments are manually recreated in
LaunchDarkly and the parallel paths are removed.

## What "manually recreated" means

LaunchDarkly does have an experimentation product, but mapping a Statsig
experiment (variants, sampling, exposure, results) to LD is a deliberate
configuration step in the LD dashboard — not a code translation. Do not
attempt to auto-generate LD experiment configs from Statsig experiment
code; that's a known foot-gun.
