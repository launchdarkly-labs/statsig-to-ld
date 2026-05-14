# `migration-summary.json` schema

Write to project root. Consumed by both the user and the static eval
harness. All fields below are required unless marked optional.

```json
{
  "timestamp": "2026-05-11T17:00:00Z",
  "skill_version": "2.0.0",

  "resolved_sdk_versions": {
    "launchdarkly-js-client-sdk": "3.9.1",
    "launchdarkly-react-client-sdk": "3.9.1",
    "@launchdarkly/node-server-sdk": "9.10.14",
    "@launchdarkly/observability": "1.1.8",
    "@launchdarkly/session-replay": "1.1.8"
  },
  "version_resolution_source": "live_npm_view",

  "sdk_key": {
    "stored_in": ".env",
    "variable_name": "LD_CLIENT_SIDE_ID",
    "gitignored": true,
    "value_logged_to_summary": false
  },

  "detected_sdks": ["javascript", "react"],
  "react_camel_case_enabled": true,

  "summary": {
    "total_items": 25,
    "successfully_migrated": 18,
    "blocked_by_experiments": 5,
    "failed": 2
  },

  "migrated": {
    "feature_gates": [
      {
        "statsig_name": "new_dashboard",
        "ld_name_raw": "new_dashboard",
        "ld_name_react_camel": "newDashboard",
        "fallback": false,
        "file": "src/Dashboard.jsx",
        "line": 42
      }
    ],
    "dynamic_configs": [
      {
        "statsig_name": "homepage_config",
        "ld_name_raw": "homepage_config",
        "ld_name_react_camel": "homepageConfig",
        "fallback_object": { "title": "Default", "enabled": false, "limit": 10 },
        "file": "src/Home.jsx",
        "line": 17
      }
    ]
  },

  "not_migrated": {
    "experiments": [
      {
        "name": "checkout_flow_test",
        "affected_gates": ["express_checkout", "new_payment_flow"],
        "reason": "Experiments require manual recreation in LaunchDarkly"
      }
    ],
    "blocked_gates": [
      {
        "name": "express_checkout",
        "reason": "Part of experiment: checkout_flow_test"
      }
    ],
    "failed_items": [
      {
        "name": "complex_gate",
        "reason": "Dynamic fallback based on user properties — needs manual review",
        "file": "src/Complex.js",
        "line": 88
      }
    ]
  },

  "observability_migration": {
    "session_replay": {
      "migrated": true,
      "statsig_params": {
        "maxSessionDurationMs": 1800000,
        "recordConsoleErrors": true,
        "privacyMask": true
      },
      "launchdarkly_params": {
        "privacySetting": "default"
      },
      "lost_features": ["maxSessionDurationMs", "recordConsoleErrors"]
    },
    "autocapture": {
      "migrated": true
    }
  },

  "verification_checklist": [
    ".env contains LD_CLIENT_SIDE_ID and is gitignored",
    "npm ls shows resolved LaunchDarkly SDK versions",
    "App boots without waitForInitialization timing out",
    "LaunchDarkly Live Events shows expected context kind + key",
    "One migrated flag serves expected variation in test env",
    "Statsig SDK still initializes if experiments remain"
  ],

  "next_steps": [
    "Run `node skills/statsig-to-launchdarkly-migrator/scripts/resolve-versions.mjs` again before next release to catch SDK bumps",
    "Recreate experiments manually in LaunchDarkly dashboard",
    "After test-env verification, repeat rollout in prod environment",
    "Remove Statsig SDK once all experiments are migrated"
  ],

  "warnings": [
    "Statsig imports preserved due to active experiments"
  ]
}
```

## Forbidden in the report

- The literal value of any SDK key (Client-Side ID, SDK Key, Statsig key)
- PII pulled from sample contexts
- Anything that would be unsafe to commit

The static eval harness rejects reports that contain any of these.
