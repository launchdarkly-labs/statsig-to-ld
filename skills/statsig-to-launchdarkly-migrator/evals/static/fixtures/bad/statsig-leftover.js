// FAIL: Statsig SDK still imported but no experiment is being used.
// The migration should have removed the Statsig import.

import statsig from 'statsig-js';
import { initialize } from 'launchdarkly-js-client-sdk';

const ldClient = initialize(process.env.LD_CLIENT_SIDE_ID, { kind: 'user', key: 'u-1' });

if (ldClient.variation('show_new_thing', false)) {
  // ...
}
