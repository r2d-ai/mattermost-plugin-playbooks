// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/require"
)

func validOfficeGateProjection() OfficeGateProjection {
	return OfficeGateProjection{
		SourceKey: "request:123:approval:456", SourceVersion: 1,
		RequestID: "request-123", ApprovalID: "approval-456", Title: "Manager approval",
		RequesterUserID: model.NewId(), CreatorUserID: model.NewId(), SubmitterUserID: model.NewId(),
		PriceConfirmerUserID: model.NewId(), AssigneeUserID: model.NewId(),
		Guards: []string{"NotRequester", "ApproverNotCreatorOrSubmitter", "NotPriceConfirmer"}, Status: "pending",
	}
}

func TestOfficeGateReviewEligibility(t *testing.T) {
	p := validOfficeGateProjection()
	for _, test := range []struct {
		name   string
		change func(*OfficeGateProjection)
		actor  string
		want   bool
	}{
		{"assigned reviewer", func(*OfficeGateProjection) {}, p.AssigneeUserID, true},
		{"another user", func(*OfficeGateProjection) {}, model.NewId(), false},
		{"unassigned admin has no bypass", func(p *OfficeGateProjection) { p.AssigneeUserID = "" }, model.NewId(), false},
		{"empty actor", func(p *OfficeGateProjection) { p.AssigneeUserID = "" }, "", false},
		{"requester cannot review", func(p *OfficeGateProjection) { p.RequesterUserID = p.AssigneeUserID }, p.AssigneeUserID, false},
		{"creator cannot review", func(p *OfficeGateProjection) { p.CreatorUserID = p.AssigneeUserID }, p.AssigneeUserID, false},
		{"submitter cannot review", func(p *OfficeGateProjection) { p.SubmitterUserID = p.AssigneeUserID }, p.AssigneeUserID, false},
		{"price confirmer cannot review", func(p *OfficeGateProjection) { p.PriceConfirmerUserID = p.AssigneeUserID }, p.AssigneeUserID, false},
		{"already approved", func(p *OfficeGateProjection) { p.Status = "approved" }, p.AssigneeUserID, false},
		{"unknown guard fails closed", func(p *OfficeGateProjection) { p.Guards = []string{"Unknown"} }, p.AssigneeUserID, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := p
			test.change(&projection)
			gate := OfficeGate{ID: model.NewId(), Projection: projection}
			require.Equal(t, test.want, gate.CanReview(test.actor))
			require.Equal(t, test.want, gate.View(test.actor).CanReview)
		})
	}
}

func TestOfficeGateSafeView(t *testing.T) {
	p := validOfficeGateProjection()
	gate := OfficeGate{ID: model.NewId(), RunID: model.NewId(), CallerPlugin: "company-portal", Projection: p}
	view := gate.View(p.AssigneeUserID)
	data, err := json.Marshal(view)
	require.NoError(t, err)
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Len(t, fields, 5)
	require.Equal(t, gate.ID, fields["gate_id"])
	require.Equal(t, p.Title, fields["title"])
	require.Equal(t, "pending", fields["status"])
	require.Equal(t, true, fields["can_review"])
	require.Equal(t, "/office?page=%2Fwork%2Fapprovals%2Fapproval-456", fields["path"])
	parsed, err := url.Parse(view.Path)
	require.NoError(t, err)
	require.Equal(t, "/work/approvals/approval-456", parsed.Query().Get("page"))
	private, err := json.Marshal(gate)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(private))
	require.NotContains(t, string(data), p.RequesterUserID)
	require.NotContains(t, string(data), "guards")
	require.NotEmpty(t, gate.View(model.NewId()).Path, "authorized viewers may view without decision authority")
}

func TestOfficeGateProjectionValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*OfficeGateProjection)
	}{
		{"empty source", func(p *OfficeGateProjection) { p.SourceKey = "" }},
		{"long source", func(p *OfficeGateProjection) { p.SourceKey = strings.Repeat("x", 257) }},
		{"zero version", func(p *OfficeGateProjection) { p.SourceVersion = 0 }},
		{"negative version", func(p *OfficeGateProjection) { p.SourceVersion = -1 }},
		{"empty request", func(p *OfficeGateProjection) { p.RequestID = "" }},
		{"unsafe approval", func(p *OfficeGateProjection) { p.ApprovalID = "../private?x=1" }},
		{"empty title", func(p *OfficeGateProjection) { p.Title = " " }},
		{"long title", func(p *OfficeGateProjection) { p.Title = strings.Repeat("x", 257) }},
		{"invalid actor", func(p *OfficeGateProjection) { p.RequesterUserID = "admin" }},
		{"unknown status", func(p *OfficeGateProjection) { p.Status = "complete" }},
		{"unknown guard", func(p *OfficeGateProjection) { p.Guards = []string{"IsAdmin"} }},
		{"duplicate guard", func(p *OfficeGateProjection) { p.Guards = []string{"NotRequester", "NotRequester"} }},
		{"guard missing requester", func(p *OfficeGateProjection) { p.RequesterUserID = "" }},
		{"guard missing creator", func(p *OfficeGateProjection) { p.CreatorUserID = "" }},
		{"guard missing submitter", func(p *OfficeGateProjection) { p.SubmitterUserID = "" }},
		{"guard missing price confirmer", func(p *OfficeGateProjection) { p.PriceConfirmerUserID = "" }},
		{"invalid dependent", func(p *OfficeGateProjection) { p.DependentTaskIDs = []string{"task"} }},
		{"duplicate dependent", func(p *OfficeGateProjection) { id := model.NewId(); p.DependentTaskIDs = []string{id, id} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := validOfficeGateProjection()
			test.change(&p)
			require.ErrorIs(t, p.Validate(), ErrOfficeInvalid)
		})
	}
	for _, status := range []string{"pending", "approved", "rejected", "returned", "cancelled"} {
		p := validOfficeGateProjection()
		p.Status = status
		require.NoError(t, p.Validate())
	}
	p := validOfficeGateProjection()
	p.AssigneeUserID = ""
	require.NoError(t, p.Validate(), "unassigned projections remain valid and blocked")
}

type officeProtectedTaskStore struct {
	OfficeGateStore
	runID  string
	taskID string
}

func (s officeProtectedTaskStore) BlocksTask(runID, taskID string) error {
	if runID == s.runID && taskID == s.taskID {
		return ErrOfficeBlocked
	}
	return nil
}

type officeGateCommandHost struct {
	plugin.API
	executed int
}

func (h *officeGateCommandHost) HasPermissionToChannel(_, _ string, _ *model.Permission) bool {
	return true
}

func (h *officeGateCommandHost) ExecuteCommand(_ *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	h.executed++
	return &model.CommandResponse{}, nil
}

func TestOfficeGateBlocksCommandBeforeExecution(t *testing.T) {
	item := openItem()
	item.Command = "/deploy production"
	h := newTaskStateService(t, item)
	h.svc.SetOfficeGateStore(officeProtectedTaskStore{runID: "run1", taskID: item.ID})
	host := &officeGateCommandHost{}
	h.svc.pluginAPI = pluginapi.NewClient(host, nil)
	_, err := h.svc.RunChecklistItemSlashCommand("run1", "user1", 0, 0)
	require.ErrorIs(t, err, ErrOfficeBlocked)
	require.Zero(t, host.executed, "the external command must not execute before checking the gate")
	require.Zero(t, h.store.updates)
}

func TestOfficeGateBlocksDuplicationAtSource(t *testing.T) {
	for _, test := range []struct {
		name      string
		duplicate func(*PlaybookRunServiceImpl) error
	}{
		{"task", func(s *PlaybookRunServiceImpl) error { return s.DuplicateChecklistItem("run1", "user1", 0, 0) }},
		{"checklist", func(s *PlaybookRunServiceImpl) error { return s.DuplicateChecklist("run1", "user1", 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := openItem()
			h := newTaskStateService(t, item)
			h.svc.SetOfficeGateStore(officeProtectedTaskStore{runID: "run1", taskID: item.ID})
			require.ErrorIs(t, test.duplicate(h.svc), ErrOfficeBlocked)
			require.Zero(t, h.store.updates)
			require.Len(t, h.store.run.Checklists, 1)
			require.Len(t, h.store.run.Checklists[0].Items, 1)
		})
	}
}

type officePropertyGateStore struct {
	OfficeGateStore
	gates []OfficeGate
	err   error
}

func (s officePropertyGateStore) List(_ string) ([]OfficeGate, error) {
	return s.gates, s.err
}

type officeGateConditions struct {
	ConditionService
	items []Condition
	err   error
}

func (s officeGateConditions) GetRunConditions(_, _, _ string, _, _ int) (*GetConditionsResults, error) {
	return &GetConditionsResults{Items: s.items}, s.err
}

func TestOfficeGatePropertyGuard(t *testing.T) {
	for _, test := range []struct {
		name           string
		assigneeField  string
		conditionField string
		status         string
		want           error
	}{
		{"direct assignee field", "field", "", "pending", ErrOfficeBlocked},
		{"parent assignee field", "parent", "", "pending", ErrOfficeBlocked},
		{"unrelated assignee field", "other", "", "pending", nil},
		{"approved dependency", "field", "", "approved", nil},
		{"direct condition field", "", "field", "pending", ErrOfficeBlocked},
		{"parent condition field", "", "parent", "returned", ErrOfficeBlocked},
		{"unrelated condition field", "", "other", "pending", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := openItem()
			if test.assigneeField != "" {
				item.AssigneeType = AssigneeTypePropertyUser
				item.AssigneePropertyFieldID = test.assigneeField
			}
			conditions := officeGateConditions{}
			if test.conditionField != "" {
				item.ConditionID = "condition"
				conditions.items = []Condition{{ID: "condition", ConditionExpr: &ConditionExprV1{Is: &ComparisonCondition{FieldID: test.conditionField, Value: json.RawMessage(`"yes"`)}}}}
			}
			h := newTaskStateService(t, item)
			h.svc.conditionService = conditions
			h.svc.SetOfficeGateStore(officePropertyGateStore{gates: []OfficeGate{{Projection: OfficeGateProjection{Status: test.status, DependentTaskIDs: []string{item.ID}}}}})
			err := h.svc.checkOfficePropertyChange("user1", h.store.run, "field", "parent")
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			} else {
				require.NoError(t, err)
			}
		})
	}
	t.Run("independent task does not block property changes", func(t *testing.T) {
		item := openItem()
		item.AssigneeType, item.AssigneePropertyFieldID = AssigneeTypePropertyUser, "field"
		h := newTaskStateService(t, item)
		h.svc.SetOfficeGateStore(officePropertyGateStore{gates: []OfficeGate{{Projection: OfficeGateProjection{Status: "pending", DependentTaskIDs: []string{"another-task"}}}}})
		require.NoError(t, h.svc.checkOfficePropertyChange("user1", h.store.run, "field", "parent"))
	})
	t.Run("dependency errors fail closed", func(t *testing.T) {
		unavailable := errors.New("projection unavailable")
		item := openItem()
		item.ConditionID = "condition"
		h := newTaskStateService(t, item)
		h.svc.SetOfficeGateStore(officePropertyGateStore{err: unavailable})
		require.ErrorIs(t, h.svc.checkOfficePropertyChange("user1", h.store.run, "field", "parent"), unavailable)
		h.svc.SetOfficeGateStore(officePropertyGateStore{gates: []OfficeGate{{Projection: OfficeGateProjection{Status: "pending", DependentTaskIDs: []string{item.ID}}}}})
		h.svc.conditionService = officeGateConditions{err: unavailable}
		require.ErrorIs(t, h.svc.checkOfficePropertyChange("user1", h.store.run, "field", "parent"), unavailable)
	})
}

type officeGatePropertyValues struct {
	PropertyService
	field  PropertyField
	value  PropertyValue
	writes int
}

func (s *officeGatePropertyValues) GetRunPropertyFields(_ string) ([]PropertyField, error) {
	return []PropertyField{s.field}, nil
}

func (s *officeGatePropertyValues) GetRunPropertyValues(_ string) ([]PropertyValue, error) {
	return []PropertyValue{s.value}, nil
}

func (s *officeGatePropertyValues) UpsertRunPropertyValue(_, _ string, value json.RawMessage) (*PropertyValue, error) {
	s.writes++
	s.value.Value = value
	return nil, errors.New("unexpected host property write")
}

func TestOfficeGateBlocksPropertyBeforeHostWrite(t *testing.T) {
	item := openItem()
	item.AssigneeType, item.AssigneePropertyFieldID = AssigneeTypePropertyUser, "field"
	h := newTaskStateService(t, item)
	oldValue := json.RawMessage(`"reviewer"`)
	properties := &officeGatePropertyValues{
		field: PropertyField{PropertyField: model.PropertyField{ID: "field", Type: model.PropertyFieldTypeUser}},
		value: PropertyValue{FieldID: "field", Value: oldValue},
	}
	h.svc.propertyService = properties
	h.svc.licenseChecker = allowAllLicenseChecker{}
	h.svc.SetOfficeGateStore(officePropertyGateStore{gates: []OfficeGate{{Projection: OfficeGateProjection{Status: "pending", DependentTaskIDs: []string{item.ID}}}}})
	_, err := h.svc.SetRunPropertyValue("user1", "run1", "field", json.RawMessage(`""`))
	require.ErrorIs(t, err, ErrOfficeBlocked)
	require.Zero(t, properties.writes)
	require.JSONEq(t, string(oldValue), string(properties.value.Value))
	require.Zero(t, h.store.updates)
}
