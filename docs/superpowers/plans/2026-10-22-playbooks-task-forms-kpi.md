# Playbooks forms and KPI implementation plan

Status: deferred at the user's request on 2026-10-22. Preserve this plan for the next KPI discussion; implementing Office bridge issues #2 and #3 does not complete its form/KPI tasks.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let employees and managers complete versioned KPI assessments in Playbooks, using reusable form/review capabilities that also work without Portal.

**Architecture:** Playbooks renders typed form tasks and coordinates operational task state. The built-in provider owns standalone form records; the Portal provider owns KPI records and business transitions. Provider commands use the real acting user, durable receipts, and one authoritative transaction per business mutation.

**Tech Stack:** Existing Go services, PostgreSQL migrations, React/TypeScript webapp, Mattermost PluginHTTP, Portal workflow/outbox packages. Use the repositories' pinned toolchains and dependencies.

**Spec:** [Forms and reviews in Playbooks, applied to annual KPI](../specs/2026-10-22-playbooks-task-forms-kpi-design.md).

## Global constraints

- KPI has exactly four categories: Shared, Strategic, Functional, People.
- Category weights, objectives, targets, rubrics, and team allocations are configurable. The supplied HTML is sample content, not permanent policy. People is not fixed at 10%.
- Objectives are agreed annually, with a review every six months. Approved changes to objectives and weights can take effect during the year.
- Provider selection is immutable on a started task.
- External KPI answer payloads are not stored in the local draft/submission tables.
- Ordinary tasks preserve existing behavior; controlled tasks require authoritative submission/decision receipts.
- No SQL transaction spans a PluginHTTP or Mattermost channel API call.
- The plugin host verifies callers. Browser identity fields never select the acting user or provider.
- Proposed v1 form bounds: 100 rows, 20 columns per repeated layout, 20 sections, 4000 characters per text answer, 1 MiB request/response body.
- Use testify assertions in Playbooks Go tests, per `server/AGENTS.md`.
- Record exact host/build versions and separate unit-test evidence from real-host evidence.

## Review focus

- **Midperiod goal changes:** pin effective revisions and calendar before scoring; never recompute a finalized earlier review with new weights. Covered in tasks 6 and 9.
- **Manager transfer during an open form:** reauthorize each command; preserve unsent text and reject stale assignment. Covered in tasks 2, 4, and 8.
- **Commit succeeds but response is lost:** recover the accepted command without a second submission, run, or workflow transition. Covered in tasks 1, 2, and 5.
- **Private data outside the form:** protect list counts, GraphQL, autocomplete, websocket, notifications, export, and support output. Covered in tasks 1 and 9.
- **Template edits and returned reviews:** existing attempts keep their definition and predecessor submission; a new attempt cannot inherit prior approval. Covered in tasks 2, 3, and 7.

## Delivery order and boundaries

This is an umbrella plan with three separately reviewable deliverables:

1. **Reusable Playbooks forms:** tasks 1–4. Produces standalone equipment acceptance and review with the built-in provider.
2. **Integration and KPI preparation:** task 5 and tasks 6–7. Produces the provider contract and annual individual goal lifecycle. Portal business work stays in its repository.
3. **KPI in Playbooks:** tasks 8–9. Produces semiannual provisioning, native assessment, and joint acceptance.

Dependency order: `1 → 2 → 3 → 4`; `1 → 5`; `6 → 7`; `{4,5,7} → 8 → 9`.
Task 1 includes issue #2 provisioning foundations, task 3 includes issue #3 enforcement, and tasks 5/8 supply the newly required form command contract. Issue #3's existing View/Review contract remains valid until the direct action path is verified.

Paths below are relative to the Playbooks repository unless prefixed `Portal:`. Portal changes require its applicable repository instructions and current implementation packet at execution time. This planning pass writes only documentation in the Playbooks repository; it does not update GitHub issues, either plugin, or the Portal repository.

## Task 1: Trusted bridge, recoverable provisioning, and restricted runs

**Files**

- Create `server/api/office.go`, `server/app/office.go`, `server/sqlstore/office.go` and corresponding `_test.go` files.
- Modify `server/plugin.go`, `server/api/api.go`, `server/api/playbook_runs.go`, `server/app/playbook_run_service.go`, `server/app/permissions_service.go`, `server/sqlstore/playbook_run.go`, `server/sqlstore/migrations.go`.
- Modify `server/api/graphql_run.go`, `server/api/graphql_root_run.go`, `server/api/schema.graphqls` for restricted run access where needed.
- Add the matching SQL migration asset using the next available sequence at implementation time.

**Interfaces**

- `OfficeService.CreateRun(ctx context.Context, actorID string, request OfficeRunRequest) (OfficeRunResult, error)` implements the issue #2 wire contract.
- `OfficeService.RunByKey(ctx context.Context, actorID, operationKey string) (OfficeRunResult, error)` rechecks actor and run/channel scope.
- `OfficeService.Tasks(ctx context.Context, actorID string, query OfficeTaskQuery) (OfficeTaskPage, error)` filters before limiting.
- `OfficeRunRequest`/`Result` retain the exact field names from issue #2. Additive run restriction is stored internally, not accepted from an untrusted browser identity field.
- Shared creation preparation returns a persisted plan with reserved run ID, checklist IDs, effective template snapshot, audience, and deterministic channel correlation. The bridge's create path must not call the old random-ID workflow unchanged.

**Steps**

- [ ] Add `TestOfficeCallerAndActorScope`, `TestOfficeInboxCursorAndVisibility`, `TestOfficeRunRecovery`, `TestRestrictedRunReadSurfaces`. Assert forged browser caller denial, two equal-due-date tasks paginated without omission, no hidden-task counts, same run after each crash checkpoint, and changed actor/payload denial.
- [ ] Run `go test ./server/api ./server/app ./server/sqlstore -run 'TestOffice|TestRestrictedRun' -count=1` and confirm the new assertions fail before implementation.
- [ ] Implement durable claims and a recoverable prepared/channel/run/complete lifecycle. Use unique IDs/correlation as the invariant even if an execution lock is lost. Allocate sequential identifiers once. Checkpoint template properties, conditions, memberships, and setup so a partially initialized run is not returned as complete.
- [ ] Implement explicit run audience, early ACL filtering, and safe event serialization. Reuse a suitable private channel only after audience validation; do not widen membership silently.
- [ ] Rerun the targeted tests against PostgreSQL, including reconnect/restart injection. Check existing run creation and permission tests. Commit this slice after review.

**Deliverable:** Issue #2 endpoints with truthful capability flags and tested recovery; a safe container for confidential form tasks.

## Task 2: Typed form definitions and a built-in provider

**Files**

- Create `server/app/task_forms.go`, `server/app/task_forms_local.go`, `server/sqlstore/task_forms.go`, `server/api/task_forms.go` and their tests.
- Create `client/task_forms.go` for the published transport types.
- Add migrations for local definitions, bindings, drafts, submissions, receipts, and dependencies.
- Modify `server/app/playbook.go`, `server/app/playbook_run.go`, `server/api/api.yaml`, `client/playbook.go`, `server/plugin.go`.

**Interfaces**

```go
type TaskFormProvider interface {
    GetForm(ctx context.Context, actorID string, binding TaskBinding) (FormView, error)
    Execute(ctx context.Context, actorID string, binding TaskBinding, command FormCommand) (CommandResult, error)
    LookupCommand(ctx context.Context, actorID string, binding TaskBinding, commandKey string) (CommandResult, error)
}
```

- `FormDefinition`: `id`, `version`, typed `sections`, `layout`, `validation`, optional declared `scoring`.
- `TaskBinding`: stable task/run/provider/source/definition/attempt identities and source version, with no answers.
- `FormView`: definition, authorized values, read-only predecessor submission, allowed actions, expected version, computed result, and availability.
- `FormCommand`: `command_key`, `expected_version`, `attempt_id`, `action`, typed answers, and reason where required.
- `CommandResult`: `receipt_id`, `source_version`, `outcome`, authorized view. Its durable identity is reusable after response loss.
- `ValidateFormDefinition(def FormDefinition) error` and `ValidateFormAnswers(def FormDefinition, answers FormAnswers, complete bool) error` parse known field kinds; unknown kinds/actions fail explicitly.
- Publish JSON fixtures under `testdata/task_forms/v1/` for definition, caller-filtered view, submit/replay, conflict, and error. These fixtures define the Go/TypeScript boundary.

**Steps**

- [ ] Add `TestTaskFormDraftAndSubmission`, `TestTaskFormCommandReplay`, `TestTaskFormDefinitionBounds`, `TestTaskFormActorFiltering`. Assert partial drafts allowed, missing required answers block submit, null is not score zero, manager drafts absent from employee responses, and one immutable receipt for concurrent identical submits.
- [ ] Run `go test ./server/app ./server/sqlstore ./server/api -run TestTaskForm -count=1`; confirm the tests expose the missing behavior.
- [ ] Implement immutable local definition versions and the built-in `playbooks` provider. Save draft/submission/receipt and accepted task state atomically. Keep typed fields and answers outside ordinary checklist requirement JSON.
- [ ] Implement browser endpoints from the spec using the authenticated actor and immutable stored binding. Validate aggregate size as well as per-field bounds; reject rather than truncate.
- [ ] Rerun the tests with race/concurrency cases; run `go test ./...` in `client`. Commit the complete slice after review.

**Deliverable:** Native task forms usable through APIs without Portal, with drafts, typed answers, replay, and safe actor-specific views.

## Task 3: Controlled review steps, dependencies, and bypass protection

**Files**

- Create `server/app/task_dependencies.go`, `server/app/task_dependencies_test.go`, `server/app/task_form_guards_test.go`.
- Extend `server/app/task_forms_local.go`, `server/sqlstore/task_forms.go` and tests.
- Modify `server/app/playbook_run_service.go`, `server/app/finish_outstanding_checklist_items.go`, `server/app/task_actions.go`, `server/sqlstore/playbook_run.go`, `server/command/command.go` only where actual command routing requires it.
- Inspect GraphQL mutations and bulk edit/template replacement callers before finalizing the guard inventory.

**Interfaces**

- `TaskDependency`: prerequisite task ID and accepted outcomes. Dependencies bind stable IDs, never checklist indices.
- `ValidateTaskDependencies(tasks []TaskDefinition) error`: reject unknown IDs, cycles, and cross-run edges.
- `TaskEligibility`: available/blocked, safe reason, and allowed actions. Application guards and UI consume the same result.
- Local provider `return` starts a new predecessor attempt and invalidates dependent editable attempts. `approve` references an immutable predecessor submission ID.

**Steps**

- [ ] Add `TestTaskDependenciesDoNotBlockIndependentWork`, `TestReturnedSubmissionCannotInheritApproval`, `TestControlledTaskMutationBoundaries`. Assert self-assessment available while later approval is pending, manager blocked until a valid self receipt, and every ordinary bypass route denied.
- [ ] Run `go test ./server/app ./server/sqlstore ./server/command -run 'TestTaskDepend|TestReturned|TestControlled' -count=1` and confirm failures.
- [ ] Implement per-step prerequisites and assignee/guard enforcement before side effects. Serialize projection/submission/finish decisions on the relevant aggregate in short transactions. External readiness is advisory until the provider authorizes the action.
- [ ] Add tests for skip, restore, delete, duplicate, reorder, requirements dialog, slash, keyword completion, property reassignment, template overwrite, and force-finish. Assert no fabricated approval, no premature notification, and unchanged ordinary task behavior.
- [ ] Run the focused guard suite and ordinary checklist regression tests. Commit after review.

**Deliverable:** Reusable submit → review → return/accept flows, with server-enforced prerequisites. Supplies issue #3's required protections without a global creator prohibition.

## Task 4: Author and complete forms in the Playbooks UI

**Files**

- Create `webapp/src/types/task_forms.ts`, `webapp/src/components/task_forms/{form_editor,form_renderer,form_table,task_form_panel,submission_history}.tsx` and corresponding tests.
- Modify `webapp/src/components/checklist_item/checklist_item.tsx`, `webapp/src/components/checklist_item/hover_menu.tsx`, `webapp/src/components/rhs/rhs_run_details.tsx`, `webapp/src/types/playbook.ts`, `webapp/src/client.ts`.
- Integrate the form editor into the existing playbook editor; retain the small requirements modal for ordinary tasks.
- Add localized labels through the repository's existing i18n workflow.

**Interfaces**

- `TaskFormPanel({runId, taskId, onClose})` obtains a caller-filtered view and executes commands; callers do not pass user or provider IDs.
- `FormRenderer({view, draft, onChange})` renders discriminated field kinds and grouped rows using stable IDs.
- `FormEditor` creates a new definition version, with typed fields, rubric, required rules, layout, and reviewer prerequisites. Existing runs keep their pinned version.

**Steps**

- [ ] Add UI tests for `save and resume`, `submit incomplete form`, `compare predecessor submission`, `401/403`, `409 preserve edits`, `provider timeout`, `new template version`, keyboard navigation, and narrow layout.
- [ ] Run `npm run test -- --runInBand src/components/task_forms` from `webapp`; confirm the new UI behavior is missing.
- [ ] Implement task actions as Open form, Continue draft, Review, and View submission where permitted. Render assessments directly in the run view. Show saving/saved/unsaved states and a confirmation before abandoning dirty edits. Do not place private form values in shared run Redux objects, channel events, or localStorage.
- [ ] Implement the bounded form editor and immutable publish action. Add a built-in equipment acceptance template using number/choice/rating/evidence fields and an independent reviewer.
- [ ] Run `npm run check-types`, the focused Jest suite, and UI lint from `webapp`. Exercise the two-actor equipment flow in a real host. Commit after review.

**Deliverable:** A complete non-KPI form/review experience in Playbooks, including template authoring.

## Task 5: Versioned external provider and Office projections

**Files**

- Create `server/app/task_forms_portal.go`, `server/app/task_forms_portal_test.go`, `server/api/office_form_tasks_test.go`.
- Extend `server/api/office.go`, `server/sqlstore/office.go`, and contract fixtures.
- Portal: create `internal/mattermost/task_forms.go` and tests for host identity, deadlines, responses, and fixed routes.
- Portal: extend the implementation packet and API contract with task-form v1 routes. Preserve the current bridge v1 read contract.

**Interfaces**

- `company-portal` implements the same `TaskFormProvider` interface as task 2.
- Trusted form-task projections carry caller-scoped `source_key`, immutable run/task/source associations, monotonic `source_version`, safe assignee/deadline/readiness, and definition/attempt references.
- A code-registered adapter maps GetForm/Execute/LookupCommand to fixed `/company-portal/api/v1/integrations/playbooks/task-forms/v1/...` PluginHTTP paths. Do not use the browser `/plugins/` prefix.
- Keep `task-forms-v1` capability separate from `scoped-task-inbox`, `idempotent-runs`, and `external-approval-gates`.

**Steps**

- [ ] Add `TestPortalFormProviderTrust`, `TestPortalFormProjectionReplay`, `TestPortalFormResponseLoss`, `TestPortalFormOutage`. Assert real acting user forwarded, source association immutable, out-of-order projection ignored, equal-version conflict rejected, and no local success fallback.
- [ ] Run Playbooks provider tests and Portal `go test ./internal/mattermost -run TestTaskForm -count=1`; expect missing-contract failures first.
- [ ] Implement the two adapter endpoints against shared fixtures, with 5-second call deadlines, cancellation, size caps, receipt lookup, sanitized errors, and current actor validation. Reauthorize receipt replay before reading its stored outcome.
- [ ] Complete issue #3 projection semantics and guarded action transport. View/Review remains available during staged deployment; action buttons appear only when the deployed provider advertises and implements them.
- [ ] Run both contract suites and genuine PluginHTTP/browser-spoof tests on the exact host build. Commit each repository's slice after review.

**Deliverable:** A reusable plugin-backed form interface with one decision owner and no copied KPI answers.

## Task 6: Annual KPI goals, weight revisions, and six-month windows

**Files**

- Portal: extend `internal/modules/performance/{model,service,read}.go`.
- Portal: create `internal/modules/performance/{goals,windows,goal_amendments}.go` and corresponding tests.
- Portal: add the next performance migration under `migrations/portal/` and update `internal/portalapp/performance_handlers.go`.
- Portal: extend `web/apps/portal/src/features/performance/PerformanceAdmin.tsx`; add goal agreement/amendment views and API types.
- Portal: update its applicable performance implementation packet; supersede the old template-per-cycle assumption explicitly.

**Interfaces**

- `GoalRevision`: employee/program identities, four-category weight policy, objective revision references, approver/acknowledgement, and effective interval.
- `ReviewWindow`: program identity, start/end/open/due timestamps, status, timezone, pinned aggregation/calendar policy.
- `PublishGoalRevision(ctx, principal, input)` validates authority and weight totals before making a new immutable revision effective.
- `FreezeReviewSnapshot(ctx, assignmentID)` returns the applicable ordered goal segments, scorer/rubric revisions, reviewer assignments, and calendar version.
- Preserve old flat reviews under their original schema version. Do not silently map historical categories or re-score old submissions during migration.

**Steps**

- [ ] Add `TestGoalWeightPolicies`, `TestAnnualGoalAgreement`, `TestReviewSnapshotIsolation`, `TestMidperiodGoalRevision`. Assert two teams with different weights, People changed from 10%, a zero Strategic category, two windows, and a July amendment leaving H1 unchanged.
- [ ] Run Portal `go test ./internal/modules/performance -run 'TestGoal|TestAnnual|TestReviewSnapshot|TestMidperiod' -count=1`; confirm the old flat model fails these scenarios.
- [ ] Implement individual goal plans and effective revisions with basis-point arithmetic. Enforce category sums and criterion sums independently. Snapshot parent-objective links without deriving manager scores from child scores.
- [ ] Implement department-head/leader/employee agreement authority and explicit amendments with reason and effective date. Represent missing ratings as unrated. Retain the existing flat-format reader for historical records.
- [ ] Run domain and migration integration tests. Test Portal goal-entry UI for two teams and a midyear revision. Commit after review.

**Deliverable:** Portal can agree annual per-person goals and safely revise them for semiannual reviews.

## Task 7: KPI form adapter and authoritative assessment commands

**Files**

- Portal: create `internal/modules/performance/playbooks_forms.go` and tests.
- Portal: extend `internal/modules/performance/service.go`, `internal/portalapp/performance_handlers.go`, and task-form integration routing.
- Portal: update `workflows/performance-review.yaml` and assignment adapters to support the program's optional finalization/acknowledgement policy through new published workflow versions.
- Extend `testdata/task_forms/v1/` with KPI self, manager, returned, shared-result-pending, and finalized views.

**Interfaces**

- `BuildReviewForm(ctx, principal, assignmentID, phase)` returns task 2's caller-filtered `FormView` from the pinned KPI snapshot.
- `ExecuteReviewForm(ctx, principal, assignmentID, command)` calls the existing performance application commands after resolving the current actor, phase, attempt, and expected version.
- The form adapter adds no separate score or approval transaction. Extend the domain command transaction to include its command receipt and outbox event.
- Manager input references the accepted self submission ID. Program policy controls shared-result sourcing, scale, finalizer, and result visibility.

**Steps**

- [ ] Add `TestKPIFormSchemaMapping`, `TestKPIManagerSeesSubmittedSelf`, `TestKPIFormPrivacy`, `TestKPIFormScoreAuthority`. Assert all four groups rendered, readonly targets/weights rejected on mutation, self/manager drafts isolated, and total calculated by the domain scorer.
- [ ] Run Portal `go test ./internal/modules/performance ./internal/portalapp -run TestKPIForm -count=1` and confirm the missing adapter cases fail.
- [ ] Map the agreed objective snapshot to generic sections/rows and rating/actual/evidence fields. Reuse existing Edit/Return/Finalize logic after extending snapshot lookup; remove any frontend assumption of a global template for every employee.
- [ ] Add tests for returned attempts, reviewer reassignment, direct-manager finalization versus an independent finalizer policy, late Shared results, and alternative rating scales. Self review permits the subject; independent review enforces its own guards.
- [ ] Run the provider fixtures through the native Playbooks renderer. Commit after review.

**Deliverable:** Both actors fill real KPI forms in Playbooks while Portal applies the same business rules as its existing commands.

## Task 8: Durable window opening and Playbooks orchestration

**Files**

- Portal: create `internal/modules/performance/playbooks_orchestration.go` and tests.
- Portal: extend `internal/mattermost/playbooks.go`, `internal/mattermost/plugin.go`, and existing outbox/job registration.
- Playbooks: extend the Office bridge/projection tests and assigned-task digest for controlled tasks.

**Interfaces**

- `OpenReviewWindow(ctx, principal, windowID, expectedVersion)` transactionally creates unique employee assignments and provisioning intent.
- `ReconcilePlaybooksReview(ctx, assignmentID)` provisions/looks up a run using a stable program/window/employee/generation operation key and projects stable form task identities.
- Command acceptance publishes safe state projections through the ambient transaction. Outbox receipt/version controls replay; checklist state is never the source of KPI progress.

**Steps**

- [ ] Add `TestReviewWindowProvisionOnce`, `TestReviewOutboxRestart`, `TestReviewProjectionReorder`, `TestReviewActorTransfer`, `TestPluginReviewDispatcherLifecycle`.
- [ ] Run Portal performance, Mattermost, and outbox targeted suites; confirm missing orchestration behavior fails.
- [ ] Implement opening/reconciliation and plugin-owned dispatcher startup/shutdown. Reuse existing outbox infrastructure and durable run creation. Do not depend on an undeployed standalone worker.
- [ ] Create the self/manager/optional finalize/ack tasks with the same generic template and dynamic binding. Reuse due dates, reminders, timeline, and inbox; only actionable assignees receive reminders. Put no ratings or private comments in shared messages.
- [ ] Simulate stop/restart before and after remote run creation, local receipt persistence, and projection acknowledgement. Verify one run per employee/window and one accepted command effect. Commit after review.

**Deliverable:** Six-month reviews appear in Playbooks automatically and recover after retries or plugin restart.

## Task 9: Joint acceptance, migration compatibility, and operations

**Files**

- Add Playwright scenarios under `e2e-tests/playwright/` following that package's existing runner structure.
- Add a repeatable host smoke script under `build/` that records host/plugin versions and safe assertion results.
- Add `docs/implementation/playbooks-task-forms-operations.md` with recovery, capability rollout, and upgrade/rollback limits.
- Update the two repositories' implementation packets and issue evidence only when explicitly authorized to publish those updates.

**Steps**

- [ ] Run a full standalone equipment acceptance process with author, submitter, independent reviewer, return, resubmit, acceptance, and history. Confirm no Portal dependency.
- [ ] Run the KPI process with two departments, different level/team weights, individualized goals, H1 review, July amendment, H2 review, and a manager transfer. Both assessment phases must complete inside Playbooks.
- [ ] Verify zero-weight category, missing evidence, late Shared score, a midperiod role change, stale parallel tabs, lost response, reordered projections, disabled Portal, and ordinary task regressions.
- [ ] Run `make check-style`, `make test`, and `make dist` in Playbooks. Run Portal's applicable full backend/frontend checks and plugin build from its Makefile. Record blocked infrastructure checks separately from failures.
- [ ] Exercise genuine host trust, activation, disable/re-enable, and process restart on supported MM12. Capture exact host version, repository SHAs, bundle hashes, migration versions, and each capability's acceptance status.
- [ ] Document maintenance upgrade requirements: old binaries cannot enforce controlled tasks. Rollback must stop new controlled work and keep durable form/projection records intact; never downgrade by deleting user submissions.

**Deliverable:** Reproducible evidence for the generic form platform and the KPI integration, with accurate capability advertisement and recovery instructions.

## Planning validation

This plan maps all current requirements to tasks and identifies proposed interfaces separately from existing code. It does not assert that any implementation tests or host acceptance checks have passed. The supplied sample is used as a configurable fixture, not a source of hard-coded category weights or goals.
