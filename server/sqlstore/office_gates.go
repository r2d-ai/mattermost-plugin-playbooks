// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
)

type OfficeGateStore struct {
	store *SQLStore
}

var _ app.OfficeGateStore = (*OfficeGateStore)(nil)

func NewOfficeGateStore(store *SQLStore) *OfficeGateStore {
	return &OfficeGateStore{store: store}
}

// CreateOfficeGateTables is also used by the versioned plugin migration.
func CreateOfficeGateTables(e sqlx.Ext) error {
	_, err := e.Exec(`CREATE TABLE IF NOT EXISTS IR_OfficeGate (
		ID VARCHAR(26) PRIMARY KEY,
		CallerPlugin VARCHAR(64) NOT NULL,
		SourceKey VARCHAR(256) NOT NULL,
		RunID VARCHAR(26) NOT NULL,
		RequestID VARCHAR(128) NOT NULL,
		ApprovalID VARCHAR(128) NOT NULL,
		SourceVersion BIGINT NOT NULL CHECK (SourceVersion > 0),
		ProjectionJSON JSONB NOT NULL,
		CreateAt BIGINT NOT NULL,
		UpdateAt BIGINT NOT NULL,
		UNIQUE (CallerPlugin, SourceKey)
	);
	CREATE INDEX IF NOT EXISTS IR_OfficeGate_RunID ON IR_OfficeGate (RunID)`)
	return err
}

type sqlOfficeGate struct {
	ID             string
	CallerPlugin   string
	SourceKey      string
	RunID          string
	RequestID      string
	ApprovalID     string
	SourceVersion  int64
	ProjectionJSON json.RawMessage
}

func (g sqlOfficeGate) toGate() (app.OfficeGate, error) {
	gate := app.OfficeGate{ID: g.ID, RunID: g.RunID, CallerPlugin: g.CallerPlugin}
	if err := json.Unmarshal(g.ProjectionJSON, &gate.Projection); err != nil {
		return app.OfficeGate{}, errors.Wrap(err, "invalid stored Office gate")
	}
	if err := gate.Projection.Validate(); err != nil {
		return app.OfficeGate{}, errors.Wrap(err, "invalid stored Office gate")
	}
	return gate, nil
}

func canonicalOfficeGate(p app.OfficeGateProjection) ([]byte, error) {
	p.Guards = append([]string{}, p.Guards...)
	p.DependentTaskIDs = append([]string{}, p.DependentTaskIDs...)
	sort.Strings(p.Guards)
	sort.Strings(p.DependentTaskIDs)
	return json.Marshal(p)
}

type officeGateRun struct {
	ID             string
	ChecklistsJSON json.RawMessage
	CurrentStatus  string
	EndAt          int64
	DeleteAt       int64
}

// Projection and every mutation of controlled work take this lock first. Reads
// of the projection then see the last committed version while retaining the lock.
func lockOfficeGateRun(tx *sqlx.Tx, runID string) (*officeGateRun, error) {
	var run officeGateRun
	err := tx.Get(&run, `SELECT ID, ChecklistsJSON, CurrentStatus, EndAt, DeleteAt FROM IR_Incident WHERE ID=$1 FOR UPDATE`, runID)
	return &run, err
}

func (s *OfficeGateStore) Project(caller, runID string, p app.OfficeGateProjection) (*app.OfficeGateResult, error) {
	if caller != app.OfficeCallerPlugin {
		return nil, app.ErrOfficeForbidden
	}
	if !model.IsValidId(runID) {
		return nil, app.ErrOfficeInvalid
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	data, err := canonicalOfficeGate(p)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer s.store.finalizeTransaction(tx)
	run, err := lockOfficeGateRun(tx, runID)
	if err == sql.ErrNoRows {
		return nil, app.ErrOfficeNotFound
	}
	if err != nil {
		return nil, err
	}
	if run.DeleteAt != 0 {
		return nil, app.ErrOfficeNotFound
	}
	var owner struct {
		CallerPlugin string
		RequestID    string
		Completed    bool
	}
	err = tx.Get(&owner, `SELECT CallerPlugin, RequestID, Completed FROM IR_OfficeOperation WHERE RunID=$1`, runID)
	if err == sql.ErrNoRows {
		return nil, app.ErrOfficeForbidden
	}
	if err != nil {
		return nil, err
	}
	if owner.CallerPlugin != caller || !owner.Completed {
		return nil, app.ErrOfficeForbidden
	}
	if owner.RequestID != p.RequestID {
		return nil, app.ErrOfficeConflict
	}

	var saved sqlOfficeGate
	changed := false
	selectGate := `SELECT ID, CallerPlugin, SourceKey, RunID, RequestID, ApprovalID, SourceVersion, ProjectionJSON
		FROM IR_OfficeGate WHERE CallerPlugin=$1 AND SourceKey=$2`
	err = tx.Get(&saved, selectGate, caller, p.SourceKey)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows {
		if err := validateOfficeGateRun(run, p, nil); err != nil {
			return nil, err
		}
		now := model.GetMillis()
		inserted, insertErr := tx.Exec(`INSERT INTO IR_OfficeGate
			(ID, CallerPlugin, SourceKey, RunID, RequestID, ApprovalID, SourceVersion, ProjectionJSON, CreateAt, UpdateAt)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT (CallerPlugin, SourceKey) DO NOTHING`,
			model.NewId(), caller, p.SourceKey, runID, p.RequestID, p.ApprovalID, p.SourceVersion, string(data), now)
		if insertErr != nil {
			return nil, insertErr
		}
		count, err := inserted.RowsAffected()
		if err != nil {
			return nil, err
		}
		changed = count > 0
		// Another run may have claimed this source key while our run was locked.
		if err = tx.Get(&saved, selectGate, caller, p.SourceKey); err != nil {
			return nil, err
		}
	}
	if saved.RunID != runID || saved.RequestID != p.RequestID || saved.ApprovalID != p.ApprovalID {
		return nil, app.ErrOfficeConflict
	}
	gate, err := saved.toGate()
	if err != nil {
		return nil, err
	}
	if p.SourceVersion == saved.SourceVersion {
		previous, err := canonicalOfficeGate(gate.Projection)
		if err != nil {
			return nil, err
		}
		if string(previous) != string(data) {
			return nil, app.ErrOfficeConflict
		}
	} else if p.SourceVersion > saved.SourceVersion {
		if err := validateOfficeGateRun(run, p, &gate.Projection); err != nil {
			return nil, err
		}
		// Office owns approval lifecycle, including revisions after completion.
		// Retain task history, but reopen the run atomically with the new gate so
		// it cannot remain successfully finished under a superseded approval.
		if p.Status != "approved" && (run.CurrentStatus == app.StatusFinished || run.EndAt != 0) {
			if _, err = tx.Exec(`UPDATE IR_Incident SET CurrentStatus=$1, EndAt=0 WHERE ID=$2`,
				app.StatusInProgress, runID); err != nil {
				return nil, err
			}
		}
		_, err = tx.Exec(`UPDATE IR_OfficeGate SET SourceVersion=$1, ProjectionJSON=$2, UpdateAt=$3 WHERE ID=$4`,
			p.SourceVersion, string(data), model.GetMillis(), saved.ID)
		if err != nil {
			return nil, err
		}
		saved.SourceVersion = p.SourceVersion
		gate.Projection = p
		changed = true
	}
	if changed {
		if _, err = tx.Exec(`UPDATE IR_Incident SET UpdateAt=$1 WHERE ID=$2`, model.GetMillis(), runID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &app.OfficeGateResult{GateID: saved.ID, SourceKey: saved.SourceKey, SourceVersion: saved.SourceVersion, Status: gate.Projection.Status}, nil
}

func validateOfficeGateRun(run *officeGateRun, p app.OfficeGateProjection, previous *app.OfficeGateProjection) error {
	if previous == nil && (run.CurrentStatus == app.StatusFinished || run.EndAt != 0) && p.Status != "approved" {
		return app.ErrOfficeConflict
	}
	var checklists []app.Checklist
	if err := json.Unmarshal(run.ChecklistsJSON, &checklists); err != nil {
		return err
	}
	counts := make(map[string]int)
	advanced := make(map[string]bool)
	previousDependencies := make(map[string]bool)
	if previous != nil {
		for _, id := range previous.DependentTaskIDs {
			previousDependencies[id] = true
		}
	}
	for _, checklist := range checklists {
		for _, item := range checklist.Items {
			counts[item.ID]++
			advanced[item.ID] = item.State == app.ChecklistItemStateClosed || item.State == app.ChecklistItemStateSkipped || item.ConditionAction == app.ConditionActionHidden
		}
	}
	for _, id := range p.DependentTaskIDs {
		// A task may have been completed or removed while the gate was approved.
		// Its historical association must not prevent a newer Office decision.
		if previousDependencies[id] {
			continue
		}
		if counts[id] != 1 {
			return errors.Wrap(app.ErrOfficeInvalid, "dependent task does not exist or has ambiguous identity")
		}
		if previous == nil && p.Status != "approved" && advanced[id] {
			return errors.Wrap(app.ErrOfficeConflict, "dependent task has already advanced or is hidden")
		}
	}
	return nil
}

func listOfficeGates(q sqlx.Queryer, runID string) ([]app.OfficeGate, error) {
	var rows []sqlOfficeGate
	if err := sqlx.Select(q, &rows, `SELECT ID, CallerPlugin, SourceKey, RunID, RequestID, ApprovalID, SourceVersion, ProjectionJSON
		FROM IR_OfficeGate WHERE RunID=$1 ORDER BY ID`, runID); err != nil {
		return nil, err
	}
	gates := make([]app.OfficeGate, 0, len(rows))
	for _, row := range rows {
		gate, err := row.toGate()
		if err != nil {
			return nil, err
		}
		gates = append(gates, gate)
	}
	return gates, nil
}

func (s *OfficeGateStore) List(runID string) ([]app.OfficeGate, error) {
	if !model.IsValidId(runID) {
		return nil, app.ErrOfficeInvalid
	}
	return listOfficeGates(s.store.db, runID)
}

// BlocksTask supports an early service check before commands or cloning. Store
// mutations still check the latest projection while holding the run lock.
func (s *OfficeGateStore) BlocksTask(runID, taskID string) error {
	gates, err := s.List(runID)
	if err != nil {
		return err
	}
	for _, gate := range gates {
		if gate.Projection.Status == "approved" {
			continue
		}
		for _, id := range gate.Projection.DependentTaskIDs {
			if id == taskID {
				return app.ErrOfficeBlocked
			}
		}
	}
	return nil
}

func protectOfficeGateMutation(tx *sqlx.Tx, runID string, checklistsJSON []byte, finishing bool) error {
	run, err := lockOfficeGateRun(tx, runID)
	if err == sql.ErrNoRows {
		// Preserve the ordinary store's no-op behavior for nonexistent runs.
		return nil
	}
	if err != nil {
		return err
	}
	gates, err := listOfficeGates(tx, runID)
	if err != nil {
		return err
	}
	protected := make(map[string]bool)
	for _, gate := range gates {
		if gate.Projection.Status == "approved" {
			continue
		}
		if finishing {
			return app.ErrOfficeBlocked
		}
		for _, id := range gate.Projection.DependentTaskIDs {
			protected[id] = true
		}
	}
	if checklistsJSON == nil || len(protected) == 0 {
		return nil
	}
	var before, after []app.Checklist
	if err := json.Unmarshal(run.ChecklistsJSON, &before); err != nil {
		return err
	}
	if err := json.Unmarshal(checklistsJSON, &after); err != nil {
		return err
	}
	beforeItems := officeGateItems(before)
	afterItems := officeGateItems(after)
	for id := range protected {
		old, current := beforeItems[id], afterItems[id]
		if len(old) == 0 && len(current) == 0 {
			// A previously approved dependency may have been deleted before a
			// later Office revision. Unrelated work is still editable.
			continue
		}
		if len(old) != 1 || len(current) != 1 || !reflect.DeepEqual(old[0], current[0]) {
			return app.ErrOfficeBlocked
		}
		// A new-ID copy must not escape the dependency of the source task.
		original := old[0]
		original.ID = ""
		for newID, items := range afterItems {
			if len(beforeItems[newID]) > 0 {
				continue
			}
			for _, item := range items {
				item.ID = ""
				if reflect.DeepEqual(original, item) {
					return app.ErrOfficeBlocked
				}
			}
		}
	}
	return nil
}

func officeGateItems(checklists []app.Checklist) map[string][]app.ChecklistItem {
	items := make(map[string][]app.ChecklistItem)
	for _, checklist := range checklists {
		for _, item := range checklist.Items {
			// Reordering changes the timestamp without changing the controlled task.
			item.UpdateAt = 0
			if len(item.Requirements) == 0 {
				item.Requirements = nil
			}
			if len(item.TaskActions) == 0 {
				item.TaskActions = nil
			}
			items[item.ID] = append(items[item.ID], item)
		}
	}
	return items
}

// The GraphQL store accepts a column map, so inspect its controlled columns too.
func protectOfficeGateGraphqlMutation(tx *sqlx.Tx, runID string, setmap map[string]interface{}) error {
	var checklistsJSON []byte
	finishing := false
	for key, value := range setmap {
		switch strings.ToLower(key) {
		case "checklistsjson":
			switch v := value.(type) {
			case []byte:
				checklistsJSON = v
			case json.RawMessage:
				checklistsJSON = v
			case string:
				checklistsJSON = []byte(v)
			default:
				return app.ErrOfficeInvalid
			}
			if len(checklistsJSON) == 0 {
				return app.ErrOfficeInvalid
			}
		case "currentstatus":
			finishing = finishing || value != app.StatusInProgress
		case "endat", "deleteat":
			// Reject expressions and unrecognized values as well as nonzero times.
			finishing = finishing || (value != int64(0) && value != int(0))
		case "id":
			// Moving a row would detach its gate identities.
			if value != runID {
				return app.ErrOfficeInvalid
			}
		}
	}
	return protectOfficeGateMutation(tx, runID, checklistsJSON, finishing)
}
