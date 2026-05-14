# SDK key setup via `ldcli` (Phase 2 of the migration)

The migrated code reads the LaunchDarkly Client-Side ID from an environment
variable. This document is the playbook for getting that ID out of LaunchDarkly
and into `.env` without the value ever passing through Claude.

## Why not just hardcode the key?

1. Client-Side IDs differ per environment (test, prod). Hardcoding makes
   per-env deploys error-prone.
2. The Statsig key (`client-...`) and the LaunchDarkly Client-Side ID are
   incompatible. The static evals fail if a literal looking like either one
   shows up in migrated source.
3. Token hygiene: the token never sits in Claude's transcript or in any file
   tracked by git.

## Step 1 — Detect / install `ldcli`

```bash
node skills/statsig-to-launchdarkly-migrator/scripts/install-ldcli.mjs
```

The helper:
- runs `which ldcli`; if found, skips install
- otherwise tries `brew install launchdarkly/tap/ldcli` on macOS
- otherwise falls back to the official `curl ... | sh` installer
- never proceeds without confirming the binary is on PATH

## Step 2 — Interactive login

The user runs this themselves in their own terminal — Claude does NOT shell
into `ldcli login`, because the access token must not transit Claude:

```bash
ldcli login
```

After login, confirm with:

```bash
ldcli projects list
```

If that returns the user's projects, the session is good.

## Step 3 — Pick project and environment

```bash
ldcli projects list
ldcli environments list --project <project-key>
```

Ask the user which project and environment the migration targets. Typical
answer: the project the app currently runs against, with both a `test` and
`production` environment.

## Step 4 — Pull the Client-Side ID (or SDK Key, for server SDKs)

For browser / React migrations the migrated code uses the **Client-Side ID**:

```bash
ldcli environments get --project <project-key> --environment <env-key> --output json \
  | jq -r '.apiKey // .clientSideId // ._links | values'
```

(Use whichever field name your `ldcli` version exposes — `ldcli environments
get --help` to confirm.)

For Node.js / server-side migrations the migrated code uses the **SDK Key**
(`sdk-...`), not the Client-Side ID.

## Step 5 — Write to `.env`

```bash
node skills/statsig-to-launchdarkly-migrator/scripts/write-env.mjs \
  --var LD_CLIENT_SIDE_ID \
  --from-stdin
```

The helper accepts the key on stdin (so it never appears as a process
argument), writes it to `.env` (creating the file if needed), and refuses to
run unless `.env` is already in `.gitignore`. It updates `.env.example` with
the variable name (but never the value) so other developers know what to set.

Names by SDK shape:

| SDK | Env var name |
| --- | --- |
| `launchdarkly-js-client-sdk` (browser) | `LD_CLIENT_SIDE_ID` |
| `launchdarkly-react-client-sdk` | `LD_CLIENT_SIDE_ID` (or `NEXT_PUBLIC_LD_CLIENT_SIDE_ID` for Next.js) |
| `@launchdarkly/node-server-sdk` | `LD_SDK_KEY` |

## Step 6 — Verify

```bash
test -n "$(grep ^LD_CLIENT_SIDE_ID= .env)" && echo "env var present"
grep -q '^\.env$' .gitignore && echo "gitignored"
```

The migrated init code should read `process.env.LD_CLIENT_SIDE_ID` (or
`LD_SDK_KEY`), never a literal.

## What Claude must NOT do

- Do not run `ldcli login` from a tool call — the token would land in
  Claude's transcript.
- Do not echo the resolved key value back to the user as text. Confirm only
  that it was written, never what it was written to.
- Do not commit `.env`. The helper refuses to write the file if `.env` is
  not gitignored.
- Do not invent a Client-Side ID. If the user can't run `ldcli login` right
  now, leave `LD_CLIENT_SIDE_ID=` in `.env` with a `# TODO: paste from
  ldcli` comment and surface a follow-up action item.
