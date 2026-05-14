// React migration: camelCased flag names + LDProvider.

import React from 'react';
import { asyncWithLDProvider, useFlags, useLDClient } from 'launchdarkly-react-client-sdk';

async function bootstrap() {
  const LDProvider = await asyncWithLDProvider({
    clientSideID: process.env.LD_CLIENT_SIDE_ID,
    context: {
      kind: 'user',
      key: 'user-123',
      email: 'anna@example.com',
    },
  });

  return LDProvider;
}

function Dashboard() {
  const flags = useFlags();
  const ld = useLDClient();
  if (!flags.newDashboard) return null;

  const homepageConfig = flags.homepageConfig ?? {
    title: 'Default Title',
    enabled: false,
  };

  return <h1>{homepageConfig.title}</h1>;
}

export { bootstrap, Dashboard };
