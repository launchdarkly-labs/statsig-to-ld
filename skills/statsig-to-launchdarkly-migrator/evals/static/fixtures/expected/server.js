// Server-side Node migration. Uses @launchdarkly/node-server-sdk (the
// current scoped name) and reads the SDK key from env.

import { init } from '@launchdarkly/node-server-sdk';

const client = init(process.env.LD_SDK_KEY);

await client.waitForInitialization({ timeout: 5 });

export async function handleRequest(req) {
  const context = {
    kind: 'user',
    key: req.user.id,
    email: req.user.email,
  };

  const showNewCheckout = await client.boolVariation(
    'new_checkout',
    context,
    false,
  );

  const limits = await client.jsonVariation(
    'rate_limits',
    context,
    { requestsPerMinute: 60, burst: 10 },
  );

  return { showNewCheckout, limits };
}
