// FAIL: context object has `key` but no `kind`.

import { initialize } from 'launchdarkly-js-client-sdk';

const context = {
  key: 'user-123',
  email: 'anna@example.com',
};

const client = initialize(process.env.LD_CLIENT_SIDE_ID, context);
