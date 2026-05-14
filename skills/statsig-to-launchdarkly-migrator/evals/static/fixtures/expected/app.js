// A known-good migrated file. The static eval harness asserts this passes
// every rule. If you change the skill's canonical output shape, update this
// fixture in lockstep.

import { initialize } from 'launchdarkly-js-client-sdk';
import Observability from '@launchdarkly/observability';
import SessionReplay from '@launchdarkly/session-replay';

const context = {
  kind: 'user',
  key: 'user-123',
  email: 'anna@example.com',
  tier: 'premium',
  _meta: { privateAttributes: ['ssn'] },
};

const client = initialize(process.env.LD_CLIENT_SIDE_ID, context, {
  plugins: [
    new Observability({ tracingOrigins: true }),
    new SessionReplay({ privacySetting: 'default' }),
  ],
});

async function boot() {
  try {
    await client.waitForInitialization(5);
  } catch (err) {
    // fall through; variation() returns fallbacks
  }

  if (client.variation('new_dashboard', false)) {
    renderNewDashboard();
  }

  const homepageConfig = client.jsonVariation('homepage_config', {
    title: 'Default Title',
    enabled: false,
    limit: 10,
  });
  document.title = homepageConfig.title;
}

function renderNewDashboard() {}

boot();
