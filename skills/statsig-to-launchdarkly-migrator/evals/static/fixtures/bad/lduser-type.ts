// FAIL: uses the deprecated LDUser type. Should be LDContext.

import { initialize, type LDUser } from 'launchdarkly-js-client-sdk';

const user: LDUser = {
  key: 'user-123',
  email: 'anna@example.com',
};

const client = initialize(process.env.LD_CLIENT_SIDE_ID!, user);
