// FAIL: null/undefined fallbacks and a type-incoherent boolVariation.

import { initialize } from 'launchdarkly-js-client-sdk';

const client = initialize(process.env.LD_CLIENT_SIDE_ID, { kind: 'user', key: 'u-1' });

const a = client.variation('flag_a', null);            // null fallback — forbidden
const b = client.variation('flag_b', undefined);       // undefined fallback — forbidden
const c = client.boolVariation('flag_c', 0);           // wrong type
const d = client.stringVariation('flag_d', 42);        // wrong type
