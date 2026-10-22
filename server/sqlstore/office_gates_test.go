// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
)

func setupOfficeGateRun(t *testing.T) (*SQLStore, app.PlaybookRunStore, *app.PlaybookRun, app.OfficeGateProjection) {
	t.Helper()
	db := setupTestDB(t)
	store := setupSQLStore(t, db)
	runs := setupPlaybookRunStore(t, db)
	run, err := runs.CreatePlaybookRun(NewBuilder(t).WithChecklists([]int{2, 1}).ToPlaybookRun())
	require.NoError(t, err)
	p := app.OfficeGateProjection{
		SourceKey: "request:123:manager", SourceVersion: 1, RequestID: "request-123", ApprovalID: "approval-456",
		Title: "Manager approval", RequesterUserID: model.NewId(), AssigneeUserID: model.NewId(),
		Guards: []string{"NotRequester"}, Status: "pending", DependentTaskIDs: []string{run.Checklists[0].Items[0].ID},
	}
	_, err = db.Exec(`INSERT INTO IR_OfficeOperation
		(CallerPlugin, OperationKey, ActorUserID, Fingerprint, RunID, RequestID, PayloadJSON, PreparedJSON, Completed, CreateAt, UpdateAt)
		VALUES ($1, $2, $3, $4, $5, $6, '{}', '{}', true, 0, 0)`,
		app.OfficeCallerPlugin, model.NewId(), p.RequesterUserID, "fingerprint", run.ID, p.RequestID)
	require.NoError(t, err)
	return store, runs, run, p
}

func TestOfficeGateProjectionVersions(t *testing.T) {
	store, _, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	first, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.True(t, model.IsValidId(first.GateID))
	require.Equal(t, p.SourceKey, first.SourceKey)
	require.EqualValues(t, 1, first.SourceVersion)

	duplicate, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.Equal(t, first, duplicate)
	conflict := p
	conflict.Title = "Different approval"
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, conflict)
	require.ErrorIs(t, err, app.ErrOfficeConflict)

	newer := p
	newer.SourceVersion = 3
	newer.AssigneeUserID = model.NewId()
	newer.Status = "approved"
	result, err := gates.Project(app.OfficeCallerPlugin, run.ID, newer)
	require.NoError(t, err)
	require.Equal(t, first.GateID, result.GateID)
	require.EqualValues(t, 3, result.SourceVersion)
	require.Equal(t, "approved", result.Status)

	stale, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.Equal(t, result, stale)
	// A fresh store represents a plugin restart or a lost projection response.
	loaded, err := NewOfficeGateStore(store).List(run.ID)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, newer.AssigneeUserID, loaded[0].Projection.AssigneeUserID)
	require.EqualValues(t, 3, loaded[0].Projection.SourceVersion)

	t.Run("guard and dependent order is semantic", func(t *testing.T) {
		p.SourceKey = "second-approval"
		p.Guards = []string{"NotRequester", "NotPriceConfirmer"}
		p.PriceConfirmerUserID = model.NewId()
		p.DependentTaskIDs = []string{run.Checklists[0].Items[0].ID, run.Checklists[0].Items[1].ID}
		_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
		require.NoError(t, err)
		p.Guards[0], p.Guards[1] = p.Guards[1], p.Guards[0]
		p.DependentTaskIDs[0], p.DependentTaskIDs[1] = p.DependentTaskIDs[1], p.DependentTaskIDs[0]
		_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
		require.NoError(t, err)
	})
}

func TestOfficeGateProjectionOwnershipAndIdentity(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	for _, change := range []func(*app.OfficeGateProjection){
		func(p *app.OfficeGateProjection) { p.RequestID = "another-request" },
		func(p *app.OfficeGateProjection) { p.ApprovalID = "another-approval" },
	} {
		candidate := p
		candidate.SourceVersion++
		change(&candidate)
		_, err = gates.Project(app.OfficeCallerPlugin, run.ID, candidate)
		require.ErrorIs(t, err, app.ErrOfficeConflict)
	}
	_, err = gates.Project("other-plugin", run.ID, p)
	require.ErrorIs(t, err, app.ErrOfficeForbidden)
	_, err = gates.Project(app.OfficeCallerPlugin, model.NewId(), p)
	require.ErrorIs(t, err, app.ErrOfficeNotFound)
	ordinary, err := runs.CreatePlaybookRun(NewBuilder(t).ToPlaybookRun())
	require.NoError(t, err)
	_, err = gates.Project(app.OfficeCallerPlugin, ordinary.ID, p)
	require.ErrorIs(t, err, app.ErrOfficeForbidden)
	_, err = store.db.Exec(`INSERT INTO IR_OfficeOperation
		(CallerPlugin, OperationKey, ActorUserID, Fingerprint, RunID, RequestID, PayloadJSON, PreparedJSON, Completed, CreateAt, UpdateAt)
		VALUES ($1, $2, $3, 'different-operation', $4, $5, '{}', '{}', true, 0, 0)`,
		app.OfficeCallerPlugin, model.NewId(), p.RequesterUserID, ordinary.ID, p.RequestID)
	require.NoError(t, err)
	p.DependentTaskIDs = nil
	_, err = gates.Project(app.OfficeCallerPlugin, ordinary.ID, p)
	require.ErrorIs(t, err, app.ErrOfficeConflict, "source cannot migrate to another owned run")

	p.SourceKey = "unknown-dependent"
	p.DependentTaskIDs = []string{model.NewId()}
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.ErrorIs(t, err, app.ErrOfficeInvalid)
}

func TestOfficeGateBlocksDependentTaskMutations(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.ErrorIs(t, gates.BlocksTask(run.ID, run.Checklists[0].Items[0].ID), app.ErrOfficeBlocked)
	require.NoError(t, gates.BlocksTask(run.ID, run.Checklists[0].Items[1].ID))
	for _, test := range []struct {
		name   string
		mutate func(*app.PlaybookRun)
	}{
		{"complete", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].State = app.ChecklistItemStateClosed }},
		{"skip", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].State = app.ChecklistItemStateSkipped }},
		{"restore", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].LastSkipped = 100 }},
		{"edit", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].Title = "Overwritten" }},
		{"assign", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].AssigneeID = model.NewId() }},
		{"command", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].CommandLastRun = 100 }},
		{"delete", func(r *app.PlaybookRun) { r.Checklists[0].Items = r.Checklists[0].Items[1:] }},
		{"delete checklist", func(r *app.PlaybookRun) { r.Checklists = r.Checklists[1:] }},
		{"replace template", func(r *app.PlaybookRun) { r.Checklists = nil }},
		{"duplicate identity", func(r *app.PlaybookRun) {
			r.Checklists[1].Items = append(r.Checklists[1].Items, r.Checklists[0].Items[0])
		}},
		{"duplicate task", func(r *app.PlaybookRun) {
			clone := r.Checklists[0].Items[0]
			clone.ID = ""
			clone.UpdateAt = 100
			r.Checklists[1].Items = append(r.Checklists[1].Items, clone)
		}},
		{"hide", func(r *app.PlaybookRun) { r.Checklists[0].Items[0].ConditionAction = app.ConditionActionHidden }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := run.Clone()
			test.mutate(candidate)
			_, err := runs.UpdatePlaybookRun(candidate)
			require.ErrorIs(t, err, app.ErrOfficeBlocked)
		})
	}
	independent := run.Clone()
	independent.Checklists[0].Items[1].State = app.ChecklistItemStateClosed
	_, err = runs.UpdatePlaybookRun(independent)
	require.NoError(t, err)

	// Reordering or moving tasks preserves the gate's stable dependency identity.
	reordered := independent.Clone()
	reordered.Checklists[0].Items[0], reordered.Checklists[1].Items[0] = reordered.Checklists[1].Items[0], reordered.Checklists[0].Items[0]
	_, err = runs.UpdatePlaybookRun(reordered)
	require.NoError(t, err)
	p.SourceVersion++
	p.Status = "approved"
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, gates.BlocksTask(run.ID, p.DependentTaskIDs[0]))
	reordered.Checklists[1].Items[0].State = app.ChecklistItemStateClosed
	_, err = runs.UpdatePlaybookRun(reordered)
	require.NoError(t, err)
}

func TestOfficeGateFinishAndGraphqlCannotBypass(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	for i, status := range []string{"pending", "rejected", "returned", "cancelled"} {
		p.SourceVersion = int64(i + 1)
		p.Status = status
		_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
		require.NoError(t, err)
		require.ErrorIs(t, runs.FinishPlaybookRun(run.ID, 1000), app.ErrOfficeBlocked)
		require.ErrorIs(t, runs.GraphqlUpdate(run.ID, map[string]interface{}{"CurrentStatus": app.StatusFinished}), app.ErrOfficeBlocked)
		require.ErrorIs(t, runs.GraphqlUpdate(run.ID, map[string]interface{}{"EndAt": 1000}), app.ErrOfficeBlocked)
		require.ErrorIs(t, runs.GraphqlUpdate(run.ID, map[string]interface{}{"DeleteAt": 1000}), app.ErrOfficeBlocked)
		candidate := run.Clone()
		candidate.Checklists[0].Items[0].State = app.ChecklistItemStateClosed
		data, err := json.Marshal(candidate.Checklists)
		require.NoError(t, err)
		require.ErrorIs(t, runs.GraphqlUpdate(run.ID, map[string]interface{}{"ChecklistsJSON": data}), app.ErrOfficeBlocked)
	}
	require.NoError(t, runs.GraphqlUpdate(run.ID, map[string]interface{}{"Description": "Independent metadata"}))
	p.SourceVersion++
	p.Status = "approved"
	_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, runs.FinishPlaybookRun(run.ID, 1000))
	p.SourceVersion++
	p.Status = "pending"
	p.SourceKey = "late-approval"
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.ErrorIs(t, err, app.ErrOfficeConflict)

	ordinary, err := runs.CreatePlaybookRun(NewBuilder(t).WithChecklists([]int{1}).ToPlaybookRun())
	require.NoError(t, err)
	ordinary.Checklists[0].Items[0].State = app.ChecklistItemStateClosed
	_, err = runs.UpdatePlaybookRun(ordinary)
	require.NoError(t, err)
	require.NoError(t, runs.FinishPlaybookRun(ordinary.ID, 1000))
}

func TestOfficeGateConcurrentVersions(t *testing.T) {
	store, _, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	const workers = 10
	var wg sync.WaitGroup
	errors := make(chan error, workers)
	results := make(chan *app.OfficeGateResult, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(version int64) {
			defer wg.Done()
			candidate := p
			candidate.SourceVersion = version
			result, err := gates.Project(app.OfficeCallerPlugin, run.ID, candidate)
			errors <- err
			results <- result
		}(int64(i + 1))
	}
	wg.Wait()
	close(errors)
	close(results)
	for err := range errors {
		require.NoError(t, err)
	}
	var gateID string
	for result := range results {
		if gateID == "" {
			gateID = result.GateID
		}
		require.Equal(t, gateID, result.GateID)
	}
	all, err := gates.List(run.ID)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.EqualValues(t, workers, all[0].Projection.SourceVersion)
}

func TestOfficeGateFinishWaitsForProjection(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	p.Status = "approved"
	_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	// Hold the same run lock as projection while changing the persisted approval.
	tx, err := store.db.Beginx()
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	var locked string
	require.NoError(t, sqlx.Get(tx, &locked, `SELECT ID FROM IR_Incident WHERE ID=$1 FOR UPDATE`, run.ID))
	p.SourceVersion++
	p.Status = "pending"
	data, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE IR_OfficeGate SET ProjectionJSON=$1, SourceVersion=$2 WHERE RunID=$3`, data, p.SourceVersion, run.ID)
	require.NoError(t, err)
	finished := make(chan error, 1)
	go func() { finished <- runs.FinishPlaybookRun(run.ID, 1000) }()
	select {
	case err := <-finished:
		require.FailNow(t, "finish bypassed the projection transaction", "result: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case err := <-finished:
		require.ErrorIs(t, err, app.ErrOfficeBlocked)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "finish did not resume after the projection committed")
	}
}

func TestOfficeAssignedTasksIncludeInProgress(t *testing.T) {
	db := setupTestDB(t)
	runs := setupPlaybookRunStore(t, db)
	actor := model.NewId()
	run := NewBuilder(t).WithChecklists([]int{2}).ToPlaybookRun()
	run.Checklists[0].Items[0].AssigneeID = actor
	run.Checklists[0].Items[0].State = app.ChecklistItemStateInProgress
	run.Checklists[0].Items[1].AssigneeID = actor
	run.Checklists[0].Items[1].State = app.ChecklistItemStateClosed
	created, err := runs.CreatePlaybookRun(run)
	require.NoError(t, err)
	assigned, err := runs.GetRunsWithAssignedTasks(actor)
	require.NoError(t, err)
	require.Len(t, assigned, 1)
	require.Equal(t, created.ID, assigned[0].PlaybookRunID)
	require.Len(t, assigned[0].Tasks, 1)
	require.Equal(t, app.ChecklistItemStateInProgress, assigned[0].Tasks[0].State)
}

func TestOfficeGateNukeClearsProjections(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, runs.NukeDB())
	remaining, err := gates.List(run.ID)
	require.NoError(t, err)
	require.Empty(t, remaining)
	var operations int
	require.NoError(t, store.db.Get(&operations, `SELECT COUNT(*) FROM IR_OfficeOperation`))
	require.Zero(t, operations)
}

func TestOfficeGateCannotAttachToAdvancedTask(t *testing.T) {
	store, runs, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	for _, test := range []struct {
		name   string
		state  string
		hidden app.ConditionAction
	}{
		{"completed", app.ChecklistItemStateClosed, ""},
		{"skipped", app.ChecklistItemStateSkipped, ""},
		{"hidden", app.ChecklistItemStateOpen, app.ConditionActionHidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := run.Clone()
			candidate.Checklists[0].Items[0].State = test.state
			candidate.Checklists[0].Items[0].ConditionAction = test.hidden
			_, err := runs.UpdatePlaybookRun(candidate)
			require.NoError(t, err)
			projection := p
			projection.SourceKey = "approval-for-" + test.name
			_, err = gates.Project(app.OfficeCallerPlugin, run.ID, projection)
			require.ErrorIs(t, err, app.ErrOfficeConflict, "an unmet prerequisite cannot attach after dependent work has advanced")
			projection.Status = "approved"
			_, err = gates.Project(app.OfficeCallerPlugin, run.ID, projection)
			require.NoError(t, err, "a trusted approved projection can backfill already completed work")
		})
	}
}

func TestOfficeGateAuthoritativeReturnReopensRun(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "completed dependency"
		if deleted {
			name = "deleted dependency"
		}
		t.Run(name, func(t *testing.T) {
			store, runs, run, p := setupOfficeGateRun(t)
			gates := NewOfficeGateStore(store)
			p.Status = "approved"
			_, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
			require.NoError(t, err)
			if deleted {
				run.Checklists[0].Items = run.Checklists[0].Items[1:]
			} else {
				run.Checklists[0].Items[0].State = app.ChecklistItemStateClosed
			}
			_, err = runs.UpdatePlaybookRun(run)
			require.NoError(t, err)
			require.NoError(t, runs.FinishPlaybookRun(run.ID, 1000))

			p.Status = "returned"
			p.SourceVersion = 3
			projected, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
			require.NoError(t, err, "a higher Office version supersedes the local approval even after work advanced")
			require.Equal(t, "returned", projected.Status)
			stored, err := runs.GetPlaybookRun(run.ID)
			require.NoError(t, err)
			require.Equal(t, app.StatusInProgress, stored.CurrentStatus)
			require.Zero(t, stored.EndAt)
			if deleted {
				require.Len(t, stored.Checklists[0].Items, 1)
			} else {
				require.Equal(t, app.ChecklistItemStateClosed, stored.Checklists[0].Items[0].State, "projection does not erase historical completion")
			}
			require.ErrorIs(t, runs.FinishPlaybookRun(run.ID, 2000), app.ErrOfficeBlocked)
			stored.Checklists[1].Items[0].State = app.ChecklistItemStateClosed
			_, err = runs.UpdatePlaybookRun(stored)
			require.NoError(t, err, "independent tasks remain editable even when a formerly approved dependency was deleted")
			p.SourceVersion = 2
			p.Status = "approved"
			stale, err := gates.Project(app.OfficeCallerPlugin, run.ID, p)
			require.NoError(t, err)
			require.Equal(t, "returned", stale.Status)
		})
	}
}

func TestOfficeGateUpdatesRunTimestampOnlyWhenChanged(t *testing.T) {
	store, _, run, p := setupOfficeGateRun(t)
	gates := NewOfficeGateStore(store)
	_, err := store.db.Exec(`UPDATE IR_Incident SET UpdateAt=100 WHERE ID=$1`, run.ID)
	require.NoError(t, err)
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	var updatedAt int64
	require.NoError(t, store.db.Get(&updatedAt, `SELECT UpdateAt FROM IR_Incident WHERE ID=$1`, run.ID))
	require.Greater(t, updatedAt, int64(100), "new gates must invalidate the run presentation")

	_, err = store.db.Exec(`UPDATE IR_Incident SET UpdateAt=200 WHERE ID=$1`, run.ID)
	require.NoError(t, err)
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, store.db.Get(&updatedAt, `SELECT UpdateAt FROM IR_Incident WHERE ID=$1`, run.ID))
	require.EqualValues(t, 200, updatedAt, "identical projection is a true no-op")
	p.SourceVersion = 3
	p.AssigneeUserID = model.NewId()
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, store.db.Get(&updatedAt, `SELECT UpdateAt FROM IR_Incident WHERE ID=$1`, run.ID))
	require.Greater(t, updatedAt, int64(200), "reassignment invalidates review availability")
	_, err = store.db.Exec(`UPDATE IR_Incident SET UpdateAt=300 WHERE ID=$1`, run.ID)
	require.NoError(t, err)
	p.SourceVersion = 2
	_, err = gates.Project(app.OfficeCallerPlugin, run.ID, p)
	require.NoError(t, err)
	require.NoError(t, store.db.Get(&updatedAt, `SELECT UpdateAt FROM IR_Incident WHERE ID=$1`, run.ID))
	require.EqualValues(t, 300, updatedAt, "stale deliveries are a true no-op")
}
