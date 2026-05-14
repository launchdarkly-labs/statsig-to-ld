// FAIL: hallucinated method names that don't exist on the LD client.

import { initialize } from 'launchdarkly-js-client-sdk';
const client = initialize(process.env.LD_CLIENT_SIDE_ID, { kind: 'user', key: 'u-1' });

const a = client.getBoolean('flag_a', false);   // not a real method
const b = client.evaluate('flag_b', false);     // not a real method
const c = client.getConfig('cfg');              // Statsig name in an LD context
