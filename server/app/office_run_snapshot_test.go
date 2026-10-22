// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type officeSnapshotLicense struct{ stubLicenseChecker }

func (officeSnapshotLicense) PlaybookAttributesAllowed() bool   { return true }
func (officeSnapshotLicense) ConditionalPlaybooksAllowed() bool { return true }

type officeSnapshotConditionStore struct {
	ConditionStore
	template           []Condition
	run                []Condition
	loseCreateResponse bool
	creates            int
}

func (s *officeSnapshotConditionStore) GetPlaybookConditions(string, int, int) ([]Condition, error) {
	return s.template, nil
}
func (s *officeSnapshotConditionStore) GetRunConditions(string, string, int, int) ([]Condition, error) {
	return s.run, nil
}
func (s *officeSnapshotConditionStore) CreateCondition(_ string, c Condition) (*Condition, error) {
	s.creates++
	s.run = append(s.run, c)
	if s.loseCreateResponse {
		s.loseCreateResponse = false
		return nil, errors.New("condition response lost")
	}
	return &c, nil
}

func TestOfficeSnapshotFreezesPropertiesConditionsAndRecoversLostResponses(t *testing.T) {
	api := &plugintest.API{}
	groupID, playbookID, runID, fieldID, conditionID := model.NewId(), model.NewId(), model.NewId(), model.NewId(), model.NewId()
	template := []*model.PropertyField{{ID: fieldID, GroupID: groupID, TargetType: PropertyTargetTypePlaybook, TargetID: playbookID, Name: "Approval", Type: model.PropertyFieldTypeText}}
	stored := []*model.PropertyField{}
	creates := 0
	api.On("SearchPropertyFields", groupID, mock.Anything).Return(func(_ string, opts model.PropertyFieldSearchOpts) ([]*model.PropertyField, error) {
		if opts.TargetType == PropertyTargetTypePlaybook {
			return template, nil
		}
		return stored, nil
	})
	api.On("CreatePropertyField", mock.Anything).Return(func(field *model.PropertyField) (*model.PropertyField, error) {
		require.Empty(t, field.ID, "Mattermost refuses client-supplied property field IDs")
		creates++
		field.ID = model.NewId()
		stored = append(stored, field)
		return nil, errors.New("property response lost")
	})
	conditions := &officeSnapshotConditionStore{loseCreateResponse: true, template: []Condition{{ID: conditionID, PlaybookID: playbookID, Version: 1, ConditionExpr: &ConditionExprV1{Is: &ComparisonCondition{FieldID: fieldID, Value: json.RawMessage(`"yes"`)}}}}}
	properties := &propertyService{api: pluginapi.NewClient(api, &plugintest.Driver{}), groupID: groupID}
	backend := &OfficeRunBackendImpl{runs: &PlaybookRunServiceImpl{propertyService: properties, conditionService: &conditionService{store: conditions}, licenseChecker: officeSnapshotLicense{}}}
	p := &OfficePreparedRun{Run: &PlaybookRun{ID: runID, PlaybookID: playbookID, CreateAt: 100, Checklists: []Checklist{{Items: []ChecklistItem{{ID: model.NewId(), Title: "Conditional task", ConditionID: conditionID}}}}}, Playbook: &Playbook{ID: playbookID}}
	require.NoError(t, backend.prepareOfficeSnapshot(p))
	require.Equal(t, ConditionActionHidden, p.Run.Checklists[0].Items[0].ConditionAction, "unset initial properties must hide a condition that requires yes")
	require.NotEqual(t, conditionID, p.Run.Checklists[0].Items[0].ConditionID)
	canonical, err := json.Marshal(p)
	require.NoError(t, err)
	// Change the live playbook after preparation and restart from the saved JSON.
	template[0].Name = "Changed template"
	conditions.template = nil
	var restored OfficePreparedRun
	require.NoError(t, json.Unmarshal(canonical, &restored))
	require.NoError(t, backend.ensureOfficeSnapshot(&restored))
	require.Len(t, stored, 1)
	require.Equal(t, "Approval", stored[0].Name)
	require.Len(t, conditions.run, 1)
	require.Equal(t, stored[0].ID, conditions.run[0].ConditionExpr.(*ConditionExprV1).Is.FieldID)
	require.Equal(t, p.Run.Checklists[0].Items[0].ConditionID, conditions.run[0].ID)
	// A second process only knows the original snapshot, not the allocated host ID.
	var retry OfficePreparedRun
	require.NoError(t, json.Unmarshal(canonical, &retry))
	require.NoError(t, backend.ensureOfficeSnapshot(&retry))
	require.Equal(t, 1, creates)
	require.Equal(t, 1, conditions.creates)
	require.Equal(t, restored.Run.PropertyFields[0].ID, retry.Run.PropertyFields[0].ID)
	require.Equal(t, restored.Run.Checklists, retry.Run.Checklists)
	stored[0].DeleteAt = 1
	var deleted OfficePreparedRun
	require.NoError(t, json.Unmarshal(canonical, &deleted))
	require.ErrorIs(t, backend.ensureOfficeSnapshot(&deleted), ErrOfficeConflict)
	stored[0].DeleteAt = 0
	// An administrator's later edit must not be overwritten by recovery.
	stored[0].Name = "User changed field"
	var conflict OfficePreparedRun
	require.NoError(t, json.Unmarshal(canonical, &conflict))
	require.ErrorIs(t, backend.ensureOfficeSnapshot(&conflict), ErrOfficeConflict)
}

func TestOfficeSnapshotResumesPartialPropertyCreation(t *testing.T) {
	api := &plugintest.API{}
	groupID, playbookID, runID := model.NewId(), model.NewId(), model.NewId()
	selectID, userID, optionID, conditionID := model.NewId(), model.NewId(), model.NewId(), model.NewId()
	template := []*model.PropertyField{
		{ID: selectID, GroupID: groupID, TargetType: PropertyTargetTypePlaybook, TargetID: playbookID, Name: "Risk", Type: model.PropertyFieldTypeSelect, Attrs: model.StringInterface{model.PropertyFieldAttributeOptions: model.PropertyOptions[*model.PluginPropertyOption]{model.NewPluginPropertyOption(optionID, "High")}}},
		{ID: userID, GroupID: groupID, TargetType: PropertyTargetTypePlaybook, TargetID: playbookID, Name: "Reviewer", Type: model.PropertyFieldTypeUser},
	}
	stored := []*model.PropertyField{}
	failSecond := true
	api.On("SearchPropertyFields", groupID, mock.Anything).Return(func(_ string, opts model.PropertyFieldSearchOpts) ([]*model.PropertyField, error) {
		if opts.TargetType == PropertyTargetTypePlaybook {
			return template, nil
		}
		return stored, nil
	})
	api.On("CreatePropertyField", mock.Anything).Return(func(field *model.PropertyField) (*model.PropertyField, error) {
		if field.Name == "Reviewer" && failSecond {
			return nil, errors.New("host unavailable")
		}
		require.Empty(t, field.ID)
		field.ID = model.NewId()
		stored = append(stored, field)
		return field, nil
	})
	conditions := &officeSnapshotConditionStore{template: []Condition{{ID: conditionID, PlaybookID: playbookID, Version: 1, ConditionExpr: &ConditionExprV1{Is: &ComparisonCondition{FieldID: selectID, Value: json.RawMessage(`["` + optionID + `"]`)}}}}}
	properties := &propertyService{api: pluginapi.NewClient(api, &plugintest.Driver{}), groupID: groupID}
	backend := &OfficeRunBackendImpl{runs: &PlaybookRunServiceImpl{propertyService: properties, conditionService: &conditionService{store: conditions}, licenseChecker: officeSnapshotLicense{}}}
	p := &OfficePreparedRun{Run: &PlaybookRun{ID: runID, PlaybookID: playbookID, CreateAt: 100, Checklists: []Checklist{{Items: []ChecklistItem{{ID: model.NewId(), ConditionID: conditionID, AssigneeType: AssigneeTypePropertyUser, AssigneePropertyFieldID: userID}}}}}, Playbook: &Playbook{ID: playbookID}}
	require.NoError(t, backend.prepareOfficeSnapshot(p))
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.ErrorContains(t, backend.ensureOfficeSnapshot(p), "host unavailable")
	require.Len(t, stored, 1)
	firstID := stored[0].ID
	failSecond = false
	var restart OfficePreparedRun
	require.NoError(t, json.Unmarshal(data, &restart))
	require.NoError(t, backend.ensureOfficeSnapshot(&restart))
	require.Len(t, stored, 2)
	require.Equal(t, firstID, stored[0].ID)
	require.Equal(t, stored[1].ID, restart.Run.Checklists[0].Items[0].AssigneePropertyFieldID)
	require.Empty(t, restart.Run.Checklists[0].Items[0].AssigneeID)
	require.Len(t, conditions.run, 1)
	condition := conditions.run[0].ConditionExpr.(*ConditionExprV1)
	require.Equal(t, stored[0].ID, condition.Is.FieldID)
	var values []string
	require.NoError(t, json.Unmarshal(condition.Is.Value, &values))
	field, err := NewPropertyFieldFromMattermostPropertyField(stored[0])
	require.NoError(t, err)
	require.Equal(t, []string{field.Attrs.Options[0].GetID()}, values)
	require.NotEqual(t, optionID, values[0])
	// Reapplying reconciliation to the same object also preserves field references.
	require.NoError(t, backend.ensureOfficeSnapshot(&restart))
	require.Equal(t, stored[1].ID, restart.Run.Checklists[0].Items[0].AssigneePropertyFieldID)
}
