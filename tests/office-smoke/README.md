# Office integration smoke test

This test activates the supplied Playbooks bundle in the actual
`mattermost/mattermost-team-edition:12.0.0` Docker image with a fresh Postgres 15
database. It verifies the host binary version, genuine inter-plugin requests,
and persisted state after both plugin reactivation and a Mattermost process
restart. It does not alter a version constant in an older host.

The disposable host disables automatic prepackaged plugin installation. Without
this setting, Mattermost can replace this custom bundle with its bundled
Playbooks version during restart, which would test a different artifact.

Build the current bundle, then run:

```sh
make dist
bash tests/office-smoke/run.sh /absolute/path/to/dist/playbooks-VERSION.tar.gz
```

Requirements: Docker, Go matching the repository's `go.mod`, Python 3, curl,
and a free localhost port 18065. Set `GO=/path/to/go` if needed. The test prints
the bundle SHA256 and a result for each check. Its Docker network, containers,
and build files are removed on exit. Set `KEEP_OFFICE_SMOKE=1` to keep them for
inspection. Existing containers with these test names cause an error instead
of being replaced.

The fixture plugins (`company-portal` and `office-smoke-untrusted`) contain a
system-administrator-only relay restricted to the Playbooks Office endpoints.
They belong only in this disposable environment. The runner's target URL is
fixed to `http://127.0.0.1:18065`; it has no option for a production server.
The test account is `office-smoke-admin` / `OfficeSmoke-Only123!`.

Checks cover:

- Activation on Mattermost 12 without a license or host-version override.
- Browser header spoofing, missing actors, raw user-header overrides, untrusted caller
  plugins, and non-admin access to the test relay.
- Repeated provisioning, conflicting operation keys, recovery by operation
  key, channel name/header changes, and recovery after real process restarts.
- Assigned task pagination and rejection of user-filter overrides.
- Real property creation, condition ID remapping, hidden-task filtering, and
  preservation of property/condition snapshots after template edits and restart.
- Gate projection idempotency and source-version conflict handling.
- Safe browser fields, encoded Office links, self-review suppression, eligible
  reviewers, and channel access enforcement.
- Dependent task blocking while pending, followed by explicit completion after
  approval. An approval must not finish the run or complete the task by itself.
- A newer pending revision after explicit completion reopens the run, restores
  its auto-archived channel, retains task history, and blocks completion again.
  Replaying the older approval cannot unblock the newer pending revision.

To run the verifier again in a retained environment, reuse its fixture binary:

```sh
python3 tests/office-smoke/verify.py \
  --bundle /absolute/path/to/dist/playbooks-VERSION.tar.gz \
  --caller-binary /tmp/playbooks-office-smoke.XXXXXX/caller
```

To remove a retained smoke environment:

```sh
docker rm -fv playbooks-office-smoke-mm playbooks-office-smoke-db
docker network rm playbooks-office-smoke
```

For a browser check in a retained environment, install the existing Playwright
test dependencies under `e2e-tests/playwright` and Chromium at `/usr/bin/chromium`.
Use the run ID and reviewer username from the verifier's final JSON result:

```sh
node tests/office-smoke/browser.cjs RUN_ID
node tests/office-smoke/browser.cjs RUN_ID REVIEWER_USERNAME
```

These checks compare the gate title, status, Office link, and action label with
the authenticated API response in both backstage and the compact sidebar.
Screenshots are written to `/tmp/playbooks-office-{backstage,rhs}.png`, with a
`-reviewer` suffix for the eligible reviewer account. Any JavaScript page error
fails the check. The target remains fixed to the disposable localhost server.
