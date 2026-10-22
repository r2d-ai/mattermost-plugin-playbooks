# Office bridge v1: integration and recovery

This runbook describes the implementation for issues [#2](https://github.com/r2d-ai/mattermost-plugin-playbooks/issues/2) and [#3](https://github.com/r2d-ai/mattermost-plugin-playbooks/issues/3). Office owns business requests, approval assignment, separation of duties, and decisions. Playbooks owns runs and operational tasks.

The [earlier bridge proposal](../superpowers/specs/2026-10-22-office-bridge-design.md) includes proposed details that differ from this implementation. Native forms, direct review commands, and annual KPI policy remain deferred to the [forms and KPI design](../superpowers/specs/2026-10-22-playbooks-task-forms-kpi-design.md) and [implementation plan](../superpowers/plans/2026-10-22-playbooks-task-forms-kpi.md). Review and View open Office; this bridge has no approval callback.

## Transport and identity

Office sends requests through Mattermost `PluginHTTP` to `/playbooks/api/v0/office`. The external plugin prefix is `/plugins/playbooks/api/v0/office`; ordinary browser requests cannot call these Office routes.

| Header | Contract |
| --- | --- |
| `Mattermost-Plugin-ID` | Host-provided caller identity, exactly `company-portal`; one value required |
| `Mattermost-Plugin-Acting-User-Id` | Real acting user's Mattermost ID; one value required; user must exist and be active |
| `Mattermost-User-Id` | If present, must match the acting user; conflicting or repeated values are rejected |
| `Content-Type` | Send `application/json` for POST bodies |

Do not use a browser-supplied header as caller authentication. The host's handling of genuine PluginHTTP requests and forged browser headers is part of the real-server acceptance check below.

POST bodies are limited to 65,536 bytes. Unknown JSON fields and trailing JSON values are rejected. Query parameters must be supported by the endpoint, appear once, and have valid encoding. There is no `user_id` override.

| Method | Path below `/api/v0/office` | Result |
| --- | --- | --- |
| GET | `/capabilities` | `schema_version: 1`; capabilities `scoped-task-inbox`, `idempotent-runs`, `external-approval-gates` |
| GET | `/tasks?limit=20&cursor=…` | Acting user's visible incomplete assigned tasks |
| POST | `/runs` | Create or recover a run; HTTP 200 with `run_id`, `channel_id`, `path` |
| GET | `/runs/by-key?operation_key=…` | Same result for a completed operation, subject to current access |
| POST | `/runs/{run_id}/approval-gates` | Project one gate; HTTP 200 with `gate_id`, `source_key`, `source_version`, `status` |

The separate browser endpoint `GET /plugins/playbooks/api/v0/runs/{run_id}/approval-gates` uses normal Mattermost authentication. It returns only `gate_id`, `title`, `status`, `path`, and `can_review` for each gate. Both projection and browser reads require current run visibility **and** channel read permission.

## Scoped task inbox

`limit` defaults to 20 and must be 1–100. `cursor` is opaque, bound to the acting user, and limited to 2,048 characters. Return `next_cursor` unchanged on the next request; an empty string means the page has no continuation.

```json
{
  "items": [{
    "id": "<task-id>",
    "run_id": "<run-id>",
    "run_name": "Equipment request",
    "title": "Confirm delivery",
    "state": "open",
    "due_at": null,
    "path": "/playbooks/runs/<run-id>"
  }],
  "next_cursor": ""
}
```

Items must be assigned to the actor, visible, and incomplete in an active, undeleted run. Completed, skipped, and conditionally hidden items are excluded. Run and channel access checks happen before pagination; excluded items do not contribute a count. States are `open` or `in_progress`; due dates are Unix milliseconds or `null`.

Ordering is due date ascending, undated tasks last, then task ID and run ID. Every page checks current assignment and permissions. A cursor is a position, not a frozen snapshot or permission grant.

## Run request

String lengths below are bytes. Mattermost IDs must pass the host's 26-character ID validation. Integers are signed 64-bit values.

| Field | Requirement |
| --- | --- |
| `operation_key` | Required, 1–256 bytes; starts with ASCII letter/digit, followed by letters, digits, `_`, `.`, `:`, or `-` |
| `request_id` | Required, 1–128 ASCII letters, digits, `_`, or `-` |
| `purpose` | Required, 1–64 bytes; same character rule as `operation_key` |
| `generation`, `source_version` | Required integers, each at least 1 |
| `playbook_id`, `owner_user_id`, `team_id` | Required Mattermost IDs |
| `channel_id` | Optional Mattermost ID for an existing channel |
| `name` | At most 1,024 bytes; a new channel requires a nonblank name unless a locked playbook template supplies it |
| `summary` | Optional, at most 65,535 bytes; the total body limit still applies |

Creation checks the current playbook, team, owner, and channel permissions. The actor must be active and human. The explicit owner is retained rather than replaced with a playbook default. An existing channel must be active, in the requested team, and neither a direct nor group message channel. Appropriate channel property and membership permissions also apply.

The operation belongs to its original actor and caller plugin. Reusing its key with another actor returns 404. Changing any request field under the same key returns 409; JSON whitespace and field order do not matter. `generation` and `source_version` are fingerprint inputs, not instructions to revise an existing run. Use a new operation key for a genuinely new operation.

Completed create retries and by-key lookups recheck current run and channel access. An incomplete operation is not returned by the by-key endpoint; retry the original POST to resume it.

## Gate projection and version rules

Gate projection is allowed only on a completed Office provisioning operation owned by the caller. Its `request_id` must match that operation. Gates have their own durable IDs and table; checklist positions, local task assignment, and template edits do not define gate identity or review authority.

| Field | Requirement |
| --- | --- |
| `source_key`, `title` | Required nonblank UTF-8, at most 256 bytes each, no control characters |
| `source_version` | Required positive signed 64-bit integer |
| `request_id`, `approval_id` | Required, 1–128 ASCII letters, digits, `_`, or `-` |
| `status` | `pending`, `approved`, `rejected`, `returned`, or `cancelled` |
| `assignee_user_id` | Optional Mattermost ID; empty means nobody can review |
| `requester_user_id`, `creator_user_id`, `submitter_user_id`, `price_confirmer_user_id` | Optional Mattermost IDs, required when their guard needs them |
| `guards` | Unique values from the allowlist below; empty is allowed |
| `dependent_task_ids` | Optional protocol extension: up to 1,000 unique persisted task IDs from this run |

| Guard | Required identities | Actor must differ from |
| --- | --- | --- |
| `NotRequester` | `requester_user_id` | Requester |
| `ApproverNotCreatorOrSubmitter` | `creator_user_id`, `submitter_user_id` | Both creator and submitter |
| `NotPriceConfirmer` | `price_confirmer_user_id` | Price confirmer |

Review eligibility requires an active authorized viewer, `pending` status, exact nonempty assignee match, and every supplied guard. Admin status and run reporter identity provide no bypass. Unknown guards and missing guard identities are rejected. The browser receives safe presentation fields, not guard identities or the stored projection. Keep titles and other ordinary run/task text suitable for their authorized viewers; the bridge accepts no approval evidence, private HR reason, script, or callback URL.

The UI offers **Review** when `can_review` is true and **View request** otherwise. Both use `/office?page=` with an encoded `/work/approvals/{approval_id}`. Office must reauthorize the actual decision at that destination.

Version comparisons are scoped by `(caller plugin, source_key)`:

- An identical version and semantic payload is a no-op. Order of guards and dependency IDs is immaterial.
- An older version returns the stored version without changing it.
- Equal version with different content returns 409.
- A newer version replaces the projection. Run, request, and approval associations remain immutable. Send the complete projection, including the intended dependency list.

Only `approved` satisfies a gate. Every other status prevents run finish. When `dependent_task_ids` is supplied, those tasks are protected against edits, completion, skipping, deletion, duplication, and other controlled mutations until approval; unrelated tasks remain editable. Omitting the extension leaves the gate's finish check in place. A new dependency must identify one unambiguous task in the run; an initial non-approved projection cannot attach to already completed, skipped, or hidden work.

Projection and controlled mutations lock the same run row in their SQL transactions. A newer non-approved revision reopens a finished run while preserving task history; it does not silently erase earlier work. Host channel unarchive effects are reconciled after the projection commits and can be retried with the same projection. Reopening a run locally does not change Office's decision.

## Durable provisioning and recovery

`IR_OfficeOperation` stores the caller/key claim, original actor, canonical payload fingerprint, reserved run ID, prepared snapshot, completion flag, and timestamps. A PostgreSQL session advisory lock serializes an operation across workers. No SQL transaction spans a Mattermost API call. Resource identities and correlation checks remain necessary even if a worker loses its lock connection.

Before creating host resources, the saved preparation freezes effective playbook settings, resolved names, checklist/task IDs, property and condition definitions, initial conditional visibility, expanded invite groups, and auto-follow recipients. Retries use this saved preparation rather than a subsequently edited template. Current permissions and membership eligibility are still checked.

A new channel requests the reserved `office-<run-id>` name. Mattermost may replace that name when anonymous URLs are enabled. Recovery therefore also uses the persisted run link in the channel header and the saved creator identity. `Channel.Props` is not persisted by Mattermost and is not used as a recovery record. The host-assigned channel ID is saved in the prepared operation before membership and run creation; once bound, later name/header edits do not prevent recovery. Team, channel type, and creator must still match. Multiple matches, a collision, or an archived resource fail closed.

A durable marker is written before the host create call. If that attempt has no bound ID and neither the reserved name nor the saved run link identifies its channel, a retry returns 409 instead of issuing another create. This includes a crash just before the host call and a lost response followed by edits to recovery metadata. Mattermost has no idempotency key for channel creation, so these ambiguous cases need operator reconciliation of the actual host result and operation record. Do not clear the marker or allocate a new key without establishing whether a channel was created. Ordinary lost-response retries with intact metadata recover automatically.

Property creation reconciles host-assigned field IDs and lost responses against the saved definition; conflicting or deleted fields are not overwritten. Condition IDs remain stable.

Initial channel membership is established before inserting the run. Participant/follower initialization and the deterministic creation timeline event are retried through an atomic SQL setup step. Recovery of an existing run checks channel access before mutations and does not re-add channel members to restore revoked access. Existing participation choices and user edits are preserved.

Notifications, broadcasts, and webhooks are best effort, with no exactly-once delivery guarantee. A crash may leave an optional notification unsent. Recovering an existing run resumes core setup without replaying its creation messages or external webhooks. Durable run identity and core initialization do not imply durable delivery of every side effect.

For a timeout, lost response, or restart:

1. Keep the original actor, operation key, and exact semantic request. Do not allocate a replacement key merely because the response was lost.
2. Call by-key. A 200 provides the existing run. A 404 can mean an incomplete operation, no operation, another actor, or lost access; it is not proof that no artifact exists.
3. Retry the original POST with bounded backoff for transient failures. Recovery reuses the reserved artifacts and rechecks authorization. A projection whose response was lost is likewise retried with the same version and content.
4. Investigate 400 validation failures, 403 access failures, and 409 payload/resource conflicts before further retries. Missing or malformed acting identity returns 401; unknown server failures return 500.
5. For a resource collision or revoked access, correct the underlying configuration or permission through its normal owner. Preserve user channels, runs, tasks, and operation records; deleting them is not a recovery procedure.

An authorized database operator can inspect progress without exporting private payloads or actor/request/run IDs. Bind `$1` to the operation key; do not concatenate input:

```sql
SELECT Completed,
       PreparedJSON IS NOT NULL AS prepared,
       EXISTS (
           SELECT 1 FROM IR_Incident r
           WHERE r.ID = o.RunID AND r.DeleteAt = 0
       ) AS active_run_row_exists,
       CreateAt,
       UpdateAt
FROM IR_OfficeOperation o
WHERE CallerPlugin = 'company-portal' AND OperationKey = $1;
```

Timestamps are Unix milliseconds. `prepared = false` means no canonical snapshot has been saved; `prepared = true, completed = false` requires POST recovery. A run row alone does not establish completed setup. Avoid `SELECT *` and copying `PayloadJSON`, `PreparedJSON`, projection data, or private correlation values into support logs.

## Verification record

For this fork, disable Mattermost's automatic replacement by bundled plugins:
`PluginSettings.AutomaticPrepackagedPlugins=false` (Docker environment:
`MM_PLUGINSETTINGS_AUTOMATICPREPACKAGEDPLUGINS=false`). The Mattermost 12 image
contains a newer upstream Playbooks version and otherwise replaces this fork
on restart. The isolated smoke harness sets this explicitly.

The artifact was built on 2026-10-23 from the implementation on
`feat/office-bridge-v1`, based on `dc4c9616b506803280cf62e8c17bbf6d0d72424c`.
That base commit alone does not contain this implementation.

- Bundle: `dist/playbooks-office-bridge-v1.tar.gz`, 51,047,558 bytes; Linux amd64 and arm64.
- SHA256: `73266e680b3ab442892a5d52f29f75cc41256c38ad17a9aed307747d1ebf82ad`.
- Final app/API suites passed (`go test ./server/app ./server/api -count=1`).
- The complete SQL store suite passed in 416.360s. After the channel recovery fix, all Office tests passed in 69.951s; the final channel/operation regressions passed in 12.457s after adding the durable creation-attempt marker.
- Focused provisioning/recovery concurrency tests passed with `-race`.
- The full webapp suite passed (65 suites, 5,756 tests passed, one skipped), followed by the additional focused run-switch regression. TypeScript and changed-file ESLint/stylelint checks passed.
- Final Go lint for app/API/SQL and the smoke caller reported zero issues. The Mattermost license-header vet check passed.
- The webapp production build and final `make server bundle BUNDLE_DIR=dist BUNDLE_NAME=playbooks-office-bridge-v1.tar.gz` passed. Webpack reported its existing bundle-size warnings.

The webapp test run used the existing dependencies installed by pnpm, with a
CLI transform override for pnpm's nested package paths; repository Jest settings
were not changed:

```sh
cd webapp
pnpm --config.verifyDepsBeforeRun=false exec jest --runInBand \
  --transformIgnorePatterns 'node_modules/(?!(\.pnpm/)?(react-native|react-router|serialize-error|parse-duration)(@|/))'
```

The broader `go test ./server/... ./tests/... -count=1 -timeout=20m` did **not** pass. The root server integration package reported `TestGraphQLPropertyFieldsLicenseEnforcement` assertion failures: those existing tests expect feature denial, while the unchanged fork license checker enables those features. `TestGetOwners` also failed when a plugin copy exhausted `/tmp`, and that package reached the 20-minute timeout. The app/API/SQL and other server subpackages completed successfully. This is not a claim that the full repository test suite is green.

The final real-server smoke passed on the actual
`mattermost/mattermost-team-edition:12.0.0` image with the above bundle hash.
It verified real PluginHTTP identity handling, forged browser-header rejection,
scoped tasks, property/condition snapshots, gate permissions and dependency
blocking, plugin reactivation, and a full host process restart. Replays retained
exactly one run and channel even after editing the channel name/header. The
approved → explicit finish/archive → newer pending revision sequence reopened
the run and channel, retained task history, and blocked completion again; an
older approval could not undo the newer pending revision.

Acceptance fixture: run `mcxcxwc8utyi7ekt98fe51sape`, channel
`drhuapertbgntxw1q498y3w3ee`, both in the disposable smoke environment.
Browser acceptance also passed for both roles in the run page and compact
channel sidebar. The requester saw **Pending / View request**; the assigned
eligible reviewer saw **Pending / Review**. Both matched the authenticated API
fields, exposed no approval checkbox, and reported no JavaScript page errors.
Generated smoke logs and screenshots are retained locally under
`dist/office-smoke/`. The disposable test containers and network were removed
after evidence was saved.

Use the [isolated host smoke procedure](../../tests/office-smoke/README.md) to record genuine PluginHTTP authentication, forged browser header rejection, active-user checks, dependency enforcement, property/condition recovery, plugin re-enable, and process restart. Record the exact server image/binary version and plugin artifact used.

Repository checks to record for the final artifact include `go test ./server/app ./server/api`, PostgreSQL-backed Office store tests, focused recovery/concurrency tests with `-race`, webapp tests, style checks, and `make dist`. Unit tests and a successful bundle build do not establish host header handling or restart acceptance.
