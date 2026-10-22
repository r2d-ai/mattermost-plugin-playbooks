# Forms and reviews in Playbooks, applied to annual KPI

Status: saved for later at the user's request. The generic forms and KPI work is deferred; the current implementation covers only Office bridge issues #2 and #3, documented in the [integration runbook](../../implementation/office-bridge-v1.md).

## User requirements and precedence

The user clarified the requirements on 2026-10-22:

- KPI has exactly four categories: Shared, Strategic, Functional, People.
- Category weights, objectives, targets, rubrics, and team allocations are configurable. The supplied HTML is sample content, not permanent policy. People is not fixed at 10%.
- Objectives are agreed annually, with a review every six months. Approved changes to objectives and weights can take effect during the year.
- The department head agrees team/level weights and leader objectives. The direct leader agrees individual objectives with the employee.
- Portal can provide goal-setting and weight-editing forms. Employees perform self-assessment and direct managers perform their assessment inside Playbooks.
- Changes to Playbooks must support other processes, not introduce a KPI-specific engine into the fork.

These requirements supersede the earlier proposal to expose KPI only through links to Portal. They also supersede run-wide blocking whenever any gate is pending.

The sample is `.orca/drops/KPI-Tech-2027_1.html`. Its 1–5 scale, SMART descriptions, team examples, common Shared result, and role-transfer examples are useful initial configuration. Only the four category identities are fixed product rules. The sample file is not an executable schema or an approved callback implementation.

## Chosen architecture

Playbooks gains reusable form tasks with drafts, immutable submissions, review steps, dependencies, and a native renderer. A task provider owns the form data and commands. Two providers exercise the same interface:

- Built-in `playbooks`: Playbooks stores definitions, drafts, and submissions for standalone forms and reviews.
- Registered `company-portal`: Portal owns KPI goals, revisions, submissions, permissions, scores, and workflow state. Playbooks renders the form and sends real-user commands through authenticated PluginHTTP.

Provider selection is immutable on a started task. An outage never changes providers or creates a local copy of an external business decision. The external provider is a compiled, allowlisted adapter with fixed routes. Templates contain provider keys and bindings, not executable callbacks, JavaScript, or arbitrary URLs.

Employee interaction is entirely inside Playbooks: open the assigned task, read targets and rubrics, enter actuals/ratings/evidence, save, and submit. The manager opens the next task in Playbooks, compares the submitted self-assessment with a separate manager column, and submits or returns it. Portal is the persistence and authorization service for KPI, not a required navigation destination for assessment.

### Alternatives considered

| Option | Consequence |
| --- | --- |
| Expand existing task requirements into KPI text fields | Fast initial form, but no typed values, separate actors, private drafts, versioned submissions, or reliable weighted calculation |
| Implement a standalone KPI feature inside Playbooks | Duplicates Portal's employee hierarchy, goal policy, score ownership, and approval transactions |
| Generic Playbooks form tasks with built-in and external providers | Reuses the run experience, supports standalone processes, and keeps one owner for each business record |

The third option is selected. An iframe embedding the Portal page would not satisfy the native Playbooks requirement.

## What the code already provides

Inspected source, rather than older aspirational documents, establishes this baseline:

| Capability | Existing implementation | Planned reuse or extension |
| --- | --- | --- |
| Templates and per-person runs | `server/app/playbook_run.go`, `playbook_run_service.go` | One reusable review template, instantiated per employee and review window |
| Checklist, stable item IDs, assignee, due date | `server/app/playbook.go` | Keep operational task identity and navigation |
| Run properties | `server/app/properties.go`, `property_service.go` | Safe metadata such as window and department; not confidential answers |
| Required inputs and draft/save UI | `TaskRequirement`, `fill_requirements_modal.tsx` | Reuse UI controls and save conventions; current requirements are only label/value strings |
| Assignee-only completion | `checkAssigneeOnlyComplete` | Ordinary behavior retained; form submissions receive stricter actor rules |
| Conditional visibility | `server/app/condition.go` | Reuse for presentation; not a substitute for dependency authorization |
| Task automation | `server/app/task_actions.go` | Existing keyword-based completion must not complete controlled forms or approvals |
| Reminders and assigned-task digest | `playbook_run_service.go` | Include actionable form tasks; avoid notifying about blocked steps |
| Timeline, notifications, websocket updates | Existing run service | Publish safe operational events, without answers or private scores |
| Metrics | `MaxMetricsPerPlaybook = 4`, duration/currency/integer | Retain operational reporting; this is not the four-category KPI model |
| Portal KPI | `internal/modules/performance/{model,service,read}.go` | Reuse draft/submission/return/finalize commands and integer score arithmetic |
| Portal workflow | `workflows/performance-review.yaml` | Reuse self → leader → finalizer → acknowledgement, with policy-configurable optional steps |
| Durable jobs and outbox | Portal `internal/platform/outbox`, workflow jobs | Reuse transactional publication and retry infrastructure |

Portal's implemented KPI currently selects the template through the cycle, uses flat criteria, and saves self/leader submissions separately. It needs individual goal revisions and review windows. The larger module document describes features beyond that implemented model; those descriptions are not evidence that the features already work.

## Product flow

### Annual goal agreement in Portal

1. An authorized owner opens the annual program and configures department/team/level weight policies.
2. The department head drafts and approves leader/team objectives with SMART measurement details.
3. The direct manager derives individual objectives, adds individual scope and criteria, and agrees them with the employee.
4. The approved individual goal revision pins the weight policy, objective versions, reviewer assignment, and scoring policy.
5. The program creates two default six-month review windows. Opening dates, deadlines, and timezone are explicit data, not a hard-coded cron date.

Goal agreement uses actual reporting assignments and recorded authority, not just a global `manager` role. A reassignment is explicit and audited. The system records the employee's acknowledgement of the agreed goals separately from the manager's approval.

### Review in Playbooks

Each employee has one run per review window. A typical run contains:

1. Self-assessment form, assigned to the employee.
2. Manager assessment form, assigned to the direct manager, dependent on an accepted self submission.
3. Finalization, when required by that program's policy.
4. Result acknowledgement, when required by policy.

The four KPI categories appear as sections inside the form. They are not four automatically completable checklist steps. Keep one submission for the complete self-assessment and one for the manager assessment, with independent draft/save state.

The default assessment layout is a grouped table containing objective, weight, target, actual result, evidence, self rating, manager rating, and computed contribution. Read-only and editable columns come from the caller-specific form view. Narrow layouts render one criterion card at a time. The RHS shows a short summary and opens the full form within the Playbooks run page for long assessments.

Submitting a form completes the corresponding controlled task only after its provider accepts the command. Return creates a new attempt and preserves the earlier submitted version. Managers assess the submitted self version, not an employee's later editable draft. A result acknowledgement is distinct from agreeing with the score.

### Midyear changes

H1 review pins the goal revisions applicable to H1. After H1 review, an approved amendment can establish new H2 goals and weights. H1 scores and submissions retain their original snapshots. H2 gets a new run and its own snapshot; reopening H1 is not the way to start H2.

For changes within a review window, retain effective-dated goal revisions and divide assessment into the corresponding segments. Each segment uses its own objectives and weights. The proposed default rollup weights segment scores by working days from the program's pinned calendar. A program must select its rollup/calendar policy before opening a window that spans revisions. Missing policy or an unrated required segment prevents a final numeric result, not data entry.

Annual reporting shows H1 and H2 separately. A yearly total requires an explicitly published aggregation policy, such as equal periods or working days. No automatic averaging or summing of six-month scores is implied by opening two reviews.

## KPI model owned by Portal

The following are new or extended concepts, not existing exported types:

| Concept | Identity and contents |
| --- | --- |
| Annual program | Year, timezone, agreement dates, review windows, policy versions |
| Weight policy revision | Program, department, team, level, four category weights, approval, effective dates |
| Objective revision | Stable objective ID, category, parent objective reference, SMART details, rubric, owner and verifier |
| Individual goal revision | Employee, program, agreed objective revisions, effective interval, policy revision, approval and acknowledgement |
| Review window | Program, period start/end, open date, deadline, status |
| Review assignment | Employee, window, reviewer/finalizer snapshot, pinned goal segments, Playbooks binding |
| Assessment attempt | Phase, attempt ID, answers, referenced self submission, submitted actor/time, policy and score snapshot |
| Amendment | Prior revision, proposed change, reason, approver, acknowledgement, effective date |

All four categories remain present even if one has zero weight. Store weights as basis points with 10000 representing 100%. Category weights sum to 10000. Criterion weights are absolute shares of the whole score and sum to their category weight. This matches the sample's displayed percentages and prevents multiplying the category weight twice. A UI may offer an allocation percentage inside a category, but it derives the absolute share and displays rounding adjustments before approval.

Goal linkage expresses contribution, not arithmetic inheritance: the sum of employees' percentages is not the leader's percentage, and a department score is not automatically the mean of employee scores.

The initial scale can use 1–5 with 3 as meeting expectations. The scale and per-criterion rubric are versioned policy. Missing values remain unrated. Computed previews are distinguishable from submitted/final results. Self and manager scores remain separate; default official score is the manager result accepted by the configured finalization step. Any calibration retains the prior score and reason.

Shared may use an approved company-wide result if configured for that program. It is then a read-only contribution in both forms. This policy is configurable rather than inferred from the category name.

## Generic form contract in Playbooks

### Definition and instance

`FormDefinition` is an immutable version with stable field IDs, sections, typed fields, presentation, and validation. `FormView` is the provider's caller-filtered view of a task instance. A field has exactly one kind:

- Text or multiline text.
- Decimal number with explicit unit and bounds.
- Single choice or multiple choice.
- Boolean or date.
- Rating with scale labels/rubric.
- Evidence reference using permitted links or existing authorized file references.
- Read-only value or provider-computed result.

A repeated, grouped row layout supports criteria, inspection items, procurement lines, and competency reviews. Row IDs remain stable across sorting. For externally provided forms, the provider supplies the permitted rows; the client cannot add objectives, alter weights, or change a bound definition. A local template may explicitly allow a bounded repeatable section.

The Playbook editor gains a small form editor for sections, fields, validation, layout, and reviewer steps. Publishing creates an immutable definition version. Existing runs keep their definition snapshot when the template changes. A reviewer step can display an authorized, read-only predecessor submission alongside its own fields. This is a reusable review feature, not a special self/leader component with hard-coded KPI labels.

No raw HTML or formula scripts run from a definition. Built-in scoring initially supports declared weighted rating totals and threshold bands. External scores come from the provider. Attachments use the owning provider's existing authorization; v1 supports references, with binary upload UI as a separate extension instead of attaching private evidence to a public channel.

Proposed v1 bounds are 100 rows, 20 columns per repeated layout, 20 sections, 4000 characters per text answer, and a 1 MiB transport body/response limit. Limits are validated by both endpoints and covered by contract tests. They do not silently truncate data.

### Runtime concepts

`TaskBinding` contains the stable run/task IDs, provider key, source key, source version, definition version, attempt identity, and safe projected state. It contains no answers. A normal task with no binding retains its current API behavior.

`FormCommand` contains an idempotency key, expected source version, attempt identity, allowed action, and answers/reason where required. Supported action names are `save_draft`, `submit`, `return`, `approve`, `reject`, and `acknowledge`. The current `FormView` offers only applicable actions. The provider independently enforces them.

`CommandResult` contains a durable receipt ID, authoritative source version, outcome, and caller-safe form state. Idempotent replay reauthorizes the actor before returning the original result. The same command key with changed semantic content returns 409. A stale draft/version preserves unsent edits and prompts reconciliation.

`TaskFormProvider` exposes `GetForm`, `Execute`, and `LookupCommand`. The built-in implementation stores local submissions. The Portal implementation proxies fixed versioned PluginHTTP routes under the real host-authenticated actor. An opaque source key is scoped by the registered provider and cannot be used to look up another provider's records.

### Dependencies and ownership

A dependency references a stable task ID and an allowlisted accepted outcome. Ordered checklist position and the mutable `closed` checkbox are not evidence of an accepted submission. Dependency definitions are copied from the template and locked when the controlled process starts.

The built-in provider evaluates local form dependencies in the same transaction that accepts an action. External workflows are owned by their provider: Playbooks displays projected readiness, while Portal rechecks its current workflow at every command. There is no second Playbooks engine advancing the same KPI business state.

For local forms, a return creates a new attempt for the designated predecessor and invalidates downstream editable attempts. Submitted history stays immutable. Approval always binds to the exact predecessor submission ID; a replacement submission cannot inherit an old approval.

Only dependent steps are blocked. An independent preparation task remains available while a later approval is pending. `returned`, `rejected`, and `cancelled` are explicit outcomes and never aliases for approved. Reassignment and return invalidate stale action permissions and preserve submitted history.

## Persistence and API changes

Proposed new Playbooks tables:

- `IR_TaskFormDefinition`: versioned local form definitions.
- `IR_TaskBinding`: stable provider/task associations and safe projections.
- `IR_TaskFormDraft`: built-in provider drafts keyed by task/actor/attempt.
- `IR_TaskFormSubmission`: immutable built-in submissions and revisions.
- `IR_TaskFormCommand`: built-in command keys, fingerprints, and receipts.
- `IR_TaskDependency`: stable prerequisite and outcome requirements.
- `IR_OfficeRunOperation`: issue #2 durable provisioning lifecycle.

External KPI answer payloads are not stored in the local draft/submission tables. The run/checklist JSON exposes only safe form descriptors. Provider projection updates use version comparison and transactionally preserve task identity.

Proposed browser routes, subject to normal host authentication and run/task authorization:

- `GET /api/v0/runs/{run_id}/tasks/{task_id}/form`
- `POST /api/v0/runs/{run_id}/tasks/{task_id}/form/commands`
- `GET /api/v0/runs/{run_id}/tasks/{task_id}/form/commands/{command_key}`

Proposed trusted integration routes extend the existing Office contract additively:

- `POST /api/v0/office/runs/{run_id}/form-tasks`: immutable binding and versioned readiness projection.
- Existing `/approval-gates` remains the external approval projection route from issue #3.
- Portal implements matching provider routes under `/api/v1/integrations/playbooks/task-forms/v1/` for get, execute, and receipt lookup.

The provider routes are a proposed new contract. They are not delivered by Portal PR #116. Both implementations and shared contract fixtures must land before advertising `task-forms-v1` or enabling direct actions. Existing bridge v1 clients continue to use their published endpoints and capabilities.

## Synchronization and recovery

Opening a review window creates assignments and outbox intent in Portal's transaction. Provisioning uses a stable operation key containing program, window, employee, and generation. It does not include a title or every amendment version, which would create duplicate runs.

Issue #2 supplies durable run/channel correlation and recovery. Portal stores the returned binding and publishes task projections with monotonic source versions. Duplicate dispatch, lost responses, plugin restarts, and reordered delivery converge on the same run and tasks.

Saving or submitting inside Playbooks calls the provider synchronously with a bounded timeout. The provider commits the submission, workflow transition, command receipt, and projection outbox event together. Playbooks may display an accepted receipt while the checklist projection is still catching up. It never displays acceptance solely because the network request was sent.

If the response is lost, retry/receipt lookup uses the same command key. Authorization still applies. On provider outage, drafts that were not acknowledged remain visibly unsaved; submitted status never advances locally as a fallback. Reconciliation updates the projection without re-running the decision. Logs contain identifiers and error codes, not assessment payloads.

The Mattermost plugin process must run and stop the provisioning/outbox dispatcher as part of its lifecycle. Reusing an outbox package whose dispatcher only runs in the old standalone worker is insufficient. No new bot service, PAT, or external worker deployment is required.

## Privacy and command enforcement

KPI runs use explicit restricted visibility, enforced in read queries and application permissions. View access to the reusable playbook must not grant access to every employee's run. Reuse a suitable private review channel when possible and never widen its membership automatically.

Run summaries, search, autocomplete, GraphQL, REST, inbox, exports, followers, notification recipients, and websocket payloads must all respect the restriction. Forms additionally require provider authorization; channel membership alone never grants access to manager drafts. The provider removes unauthorized fields from the response before serialization.

Task commands validate the current actor, assignee, attempt, dependency outcome, and source version. A self-assessment intentionally permits the subject to submit. A manager assessment or finalization requires the assigned independent actor. Apply separation-of-duties guards per step, not globally to every task or creator.

Ordinary check/uncheck, slash commands, requirements dialogs, skip/restore, duplicate, template overwrite, keyword automation, bulk edit, and force-finish cannot create a form receipt or approve a gate. Completion/finish checks use accepted receipts and current external status. Reordering cannot remove dependencies. Changes to run properties cannot reassign controlled review actors.

Use application service checks before side effects and transactional store enforcement to close races. Do not emit completion messages for a write that will later be denied. Old plugin binaries that lack the guard must not process controlled runs during rollout; use a maintenance upgrade or an explicit minimum-version rollout gate.

## Delivery and acceptance

The linked implementation plan splits work into generic Playbooks capabilities, Portal goal lifecycle, and the integration. Each capability is advertised only after its own acceptance evidence.

The first end-to-end scenario is an employee and manager completing KPI entirely inside Playbooks using Portal-owned data. The second is an equipment acceptance checklist with evidence and independent review using the built-in provider, without Portal. That second scenario proves the generic implementation works beyond KPI.

Acceptance includes H1/H2 revision isolation, nondefault weights and scale, zero-weight categories, late Shared results, midperiod transfer, role reassignment, privacy through all read surfaces, invalid form schemas, stale drafts, duplicate commands, lost responses, restart recovery, and unaffected ordinary checklists. The real-host report records exact Mattermost and both plugin versions.
