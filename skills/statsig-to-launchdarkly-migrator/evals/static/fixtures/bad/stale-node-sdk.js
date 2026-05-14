// FAIL: imports the legacy unscoped Node SDK (renamed years ago).
// The skill must NEVER produce this — it's the single most common stale
// training-data artifact for LaunchDarkly migrations.

const LaunchDarkly = require('launchdarkly-node-server-sdk');

const client = LaunchDarkly.init(process.env.LD_SDK_KEY);

module.exports = { client };
