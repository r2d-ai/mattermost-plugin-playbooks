# Office bridge and external approval gates

Status: archived planning proposal. The implemented #2/#3 contract and recovery behavior are documented in the [integration runbook](../../implementation/office-bridge-v1.md). Future forms and KPI work is deferred and described by the
[Playbooks form and KPI design](2026-10-22-playbooks-task-forms-kpi-design.md)
and its [implementation plan](../plans/2026-10-22-playbooks-task-forms-kpi.md).
Preserve this proposal's transport and recovery rationale; consult the runbook for delivered behavior and the newer design for future native forms and annual KPI policy.

## Scope and approved contract

Implement [issue #2](https://github.com/r2d-ai/mattermost-plugin-playbooks/issues/2)
and [issue #3](https://github.com/r2d-ai/mattermost-plugin-playbooks/issues/3)
in this Playbooks fork. Office owns business requests, approval assignment,
separation of duties, and approval decisions. Playbooks owns operational runs
and tasks. There is no second approval command or business database.

The contract source is Office's
[implementation packet at 8f57ae1](https://github.com/r2d-ai/internal-portal/blob/8f57ae1/docs/implementation/office-playbooks-v1.md).
Both issue comments confirm that direct approval callbacks are deferred.
This version opens the existing Office approval page for Review and View.

The inspected fork HEAD is `dc4c9616`. The issue baseline is `f7747ac1`.
Host activation and restart acceptance must record the eventual build SHA and
exact Mattermost 12 version. Neither acceptance check has run yet.

## Existing implementation constraints

`server/api/api.go` promotes `Mattermost-Plugin-Acting-User-Id` for
inter-plugin requests. The Office routes need a stricter caller allowlist and
must reject a missing, malformed, inactive, or conflicting actor identity.
The host-provided `Mattermost-Plugin-ID` is the trust anchor. Actual host tests
must prove that browser requests cannot forge it.

`PlaybookRunHandler.createPlaybookRun` validates the playbook, team, channel,
owner, and creation permissions. The Office bridge must reuse those rules.
Validation currently lives partly in the HTTP handler, so the shared creation
preparation belongs in the application layer before adding another caller.

`PlaybookRunServiceImpl.CreatePlaybookRun` assigns a fresh run ID, creates a
channel, and then inserts the run. A crash between channel creation and the
insert leaves remote effects without a durable run mapping. Retrying this
method unchanged cannot meet issue #2.

`PermissionsService.RunView` permits some playbook-based visibility without
channel membership. The inbox requires both run visibility and channel read
permission. Existing broader run APIs retain their current behavior.

Checklist mutation flows include REST, dialogs, slash commands, automation,
property-based assignment, and direct SQL-store updates. UI restrictions alone
cannot protect an external gate. `FinishPlaybookRun` writes through a separate
store method, which also needs gate enforcement.

## Caller interface

Office calls fixed PluginHTTP paths under `/playbooks/api/v0/office` with its
host-verified identity `company-portal` and the real acting user header.
Browser routes use the host's normal plugin prefix. They cannot invoke Office
projection or provisioning as an ordinary authenticated user.

The bridge implements these contract endpoints:

| Method | Path below `/api/v0/office` | Result |
| --- | --- | --- |
| GET | `/capabilities` | Schema version and delivered capabilities |
| GET | `/tasks` | The actor's visible incomplete assigned tasks |
| POST | `/runs` | Durable, idempotent run creation |
| GET | `/runs/by-key` | Authorized lookup of the original run |
| POST | `/runs/{run_id}/approval-gates` | Trusted versioned Office projection |

Each endpoint bounds input, rejects unknown identity selectors, and returns
public errors without private upstream details. Mutation bodies use strict
JSON decoding with a size limit and reject trailing JSON. Capabilities are
advertised only after their tests and required acceptance checks pass.

## Scoped inbox

The inbox selects tasks assigned to the actor in unfinished, undeleted runs.
It excludes completed, skipped, conditionally hidden tasks and skipped
checklists. Both run and channel ACL checks apply before the page limit.
No `user_id` override or count of excluded tasks is returned.

Ordering is due date ascending, undated tasks last, then globally stable task
identity. A task identity includes the run ID and persisted item ID. Checklist
indices are never identities. The opaque cursor encodes a version, actor
binding, due-date position, and stable identity. Every page reapplies current
authorization and assignment. The cursor is a position, not an authorization
grant or a snapshot across edits.

The default limit is 20, valid limits are 1 through 100, and cursor input is
at most 2048 characters. Malformed, unsupported, and duplicate parameters are
rejected. The wire state is `open` or `in_progress`; existing unchecked tasks
map to `open`. Due dates are Unix milliseconds or null. Links are generated
from validated run IDs as `/playbooks/runs/{run_id}`.

## Durable run creation

### Persisted operation and prepared run

Add a versioned PostgreSQL migration for a provisioning operation table with
unique `(caller_plugin, operation_key)`, original actor, canonical payload
fingerprint, request correlation, reserved run ID, reserved channel name,
prepared creation snapshot, lifecycle phase, and timestamps.

The operation binds to its original actor. Another actor cannot reuse the
mapping, even when the payload matches. Every replay rechecks current access.
A different semantic payload for the same key returns 409. Lookup under a
different caller or denied actor does not expose whether the mapping exists.

Preparation validates bounds and IDs, resolves the playbook and audience, and
persists the effective run snapshot before remote effects. Sequential number
allocation, checklist IDs, conditional rules, and run configuration must not
be regenerated from a changed template on retry. `purpose`, `generation`,
`request_id`, and `source_version` participate in the canonical fingerprint.
The bridge requires nonempty bounded purpose and correlation values and
positive generation and source version. These are proposed validation rules
because the packet does not specify exact numeric minima or text lengths.

### Recovery lifecycle

The proposed lifecycle is `prepared`, `channel_ready`, `run_ready`, `complete`.
Phases describe durable checkpoints, not independent business decisions.

1. Authorize the actor and insert or read the unique operation claim.
2. Serialize execution for this operation across plugin instances. Use a
   PostgreSQL session advisory lock on a dedicated connection, outside a DB
   transaction. Release the connection and lock on every exit path.
3. Re-read the operation and recheck access after acquiring the lock.
4. For a new channel, use the operation's reserved channel name and persist
   the intended team, type, and correlation before creation. On retry,
   reconcile an existing channel using this durable correlation. A collision
   that cannot be proven to belong to the operation requires operator review.
5. Insert the reserved run once and advance the operation checkpoint in the
   same short database transaction. A duplicate reserved run ID is reconciled
   against the operation rather than replaced.
6. Resume required setup through independently retryable checkpoints. Record
   optional notifications separately so their failure never creates a new run.
7. Return the reserved run, channel, and fixed relative path. A lost response
   returns the same result on retry and after restart.

No SQL transaction spans a Mattermost API call. A dropped worker connection
must not allow a second worker to create another logical run. Reserved IDs,
unique inserts, and channel correlation enforce that invariant even after
lock loss. Remote effects must use the prepared snapshot.

An archived, deleted, or conflicting resource is an explicit recovery error.
Recovery never deletes a channel or run containing user data. Operator
documentation includes operation lookup, checkpoint inspection, safe retry,
and collision escalation without copying private payloads into logs.

### Alternatives

An in-memory mutex and a mapping written after creation fail across restart
and leave the channel-creation crash window open. An expiring lease alone
allows overlapping workers unless every remote effect is fenced. A durable
prepared operation plus resource correlation makes the result recoverable
even when the worker loses its lock.

## External gate projection

Add a separate projection table keyed by caller and source key, with stable
gate ID, run ID, immutable request and approval IDs, source version, canonical
fingerprint, safe title, actor IDs, allowlisted guards, status, and timestamps.
Only the trusted Office projector writes this table.

Statuses are `pending`, `approved`, `rejected`, `returned`, and `cancelled`.
An identical version and payload is a no-op. An older version returns the
current projection without a write. An equal version with different semantic
content returns 409. A later version cannot move the source key to another
run, request, or approval. Office owns terminal-state transitions and new
approval revisions.

Run ownership must be tied to the Office provisioning operation. Projection
cannot attach to an unrelated run merely because the acting user can edit it.
The request ID must match the operation's immutable request correlation.

The gate's authoritative fields remain outside editable checklist JSON.
Run responses expose only a safe presentation containing stable gate ID,
title, status, authorized action availability, and generated Office path.
They do not expose requester, creator, submitter, or price-confirmer identities
unless the current existing run interface independently requires them.
No HR reason, evidence, callback URL, or script is accepted or stored.

## Review authority and bypass prevention

Review eligibility requires a pending gate, a nonempty assignee, and the real
actor matching that assignee. Guards compare the actor with the supplied
Office requester, creator, submitter, and price confirmer as specified by the
allowlist. Run reporter and bot identities do not substitute for requester.
Admins have no decision bypass. Reassignment goes through Office's audited
command; changing local task assignment never changes a projection.

Until a versioned callback is jointly specified, the UI offers Review to
eligible actors and View request to authorized viewers. The generated link
is `/office?page=` plus an encoded `/work/approvals/{approval_id}`. Office
rechecks current assignment, lifecycle, and separation of duties on its page.
There is no local Approve or Reject action.

Gate identity and relative placement do not depend on mutable checklist
indices. Gate rows cannot be manufactured through ordinary task creation,
edited, duplicated, deleted, skipped, reopened, or overwritten from a template.
UI controls mirror server restrictions but do not enforce the invariant.

The initial run-wide blocking proposal is superseded. The newer design binds
prerequisites to stable task identities and accepted source outcomes. Only
dependent work is blocked. In particular, an employee can complete the
self-assessment while the later manager review is pending. Run finish still
requires all mandatory controlled steps to have their required outcomes.
Rejected, returned, and cancelled are distinct outcomes and do not satisfy an
approval prerequisite. The expanded provider contract must be implemented on
both sides before enabling native submission and review actions.

Store enforcement locks the run row during gate-sensitive mutations and
checks the latest projection within that transaction. Projection writes take
the same run lock, preventing a stale read from racing an assignment or status
update. Checks cover whole-checklist writes, direct finish updates, and other
mutation paths that bypass the main run update function.

Reopening a run does not change Office status. A projection dependency failure
returns an error and keeps dependent work blocked. No fallback marks a gate
approved or a task successfully complete.

## Module boundaries

`server/api/office.go` owns trusted caller middleware, bounded transport
validation, error mapping, and fixed route registration.

`server/app/office.go` owns wire-independent operation states, fingerprints,
inbox cursor rules, projection validation, and review eligibility.
The existing run service owns shared preparation and resumable creation.

`server/sqlstore/office.go` owns claims, projection version comparisons,
resource correlations, locking, and transactional enforcement. Add the schema
through the active legacy migration chain and keep migration assets compatible
with the repository's migration tests.

The run response models, Go client types, and webapp types carry the safe gate
presentation. The existing checklist UI renders a separate gate view and
fixed Office links. Ordinary checklist task rules retain their behavior in
runs without Office gates.

## Verification required before completion

| Area | Required evidence |
| --- | --- |
| Host trust | Browser forged headers denied, genuine PluginHTTP actor accepted, actor conflict and inactive user denied |
| Inbox | Wrong assignee, private channel, hidden item, skipped checklist, completed run, tied due dates, malformed cursor, actor change, and filter-before-limit |
| Provisioning | Concurrent identical calls, semantic conflict, actor change, response loss, restart, and crashes at every durable checkpoint |
| Resource recovery | Channel-created-before-checkpoint, reserved run insert replay, conflicting channel, archived resource, and no destructive compensation |
| Projection | Duplicate, lower version, equal-version conflict, immutable association, unrelated run, lost response, restart, and concurrent projection |
| Review guards | Assigned non-requester, self-requester, unassigned, unassigned admin, reporter distinction, creator, submitter, and price confirmer |
| Bypass | REST, dialog, slash, skip, restore, edit, delete, duplicate, reorder, bulk overwrite, finish, property assignment, and automation |
| Races | Projection versus task completion and run finish, stale assignment, and Office page reauthorization |
| Compatibility | Existing ordinary checklist suites, reproducible migrations, Go tests, webapp tests, style checks, and plugin build |
| Real host | Exact MM12 version and fork SHA, activation, genuine inter-plugin requests, disable and re-enable, and process restart |

Capability advertisement follows delivered acceptance. A passing unit test
alone does not prove host header stripping or real restart recovery. Missing
host evidence remains explicit and prevents claiming full contract acceptance.
