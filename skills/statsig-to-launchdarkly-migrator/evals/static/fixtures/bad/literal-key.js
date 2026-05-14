// FAIL: literal Client-Side ID and a stray Statsig key.

import { initialize } from 'launchdarkly-js-client-sdk';

const ldClient = initialize('abc123def456abcdef0123456789abcd', {
  kind: 'user',
  key: 'user-1',
});

// Worse: someone copy-pasted the Statsig key in
const statsigKey = 'client-abcdef1234567890abcdef1234567890';
