// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/mattermost/mattermost/server/public/model"
)

// OfficeRunSnapshot contains resolved definitions, never references to a live
// template. Field IDs are logical IDs until Mattermost allocates the host IDs.
type OfficeRunSnapshot struct {
	Version    int                       `json:"version"`
	Fields     []PropertyField           `json:"fields"`
	Conditions []OfficeSnapshotCondition `json:"conditions"`
}

// The concrete expression makes the durable JSON decodable after a restart.
type OfficeSnapshotCondition struct {
	ID         string          `json:"id"`
	Expression ConditionExprV1 `json:"expression"`
	CreateAt   int64           `json:"create_at"`
}

type officeSnapshotProperties interface {
	prepareOfficeProperties(playbookID, runID string) (*PropertyCopyResult, error)
	ensureOfficeProperties(runID string, fields []PropertyField) (*PropertyCopyResult, error)
}

type officeSnapshotConditions interface {
	getOfficeTemplateConditions(playbookID string) ([]Condition, error)
	ensureOfficeConditions(playbookID, runID string, conditions []Condition) error
}

func (b *OfficeRunBackendImpl) prepareOfficeSnapshot(p *OfficePreparedRun) error {
	properties, ok := b.runs.propertyService.(officeSnapshotProperties)
	if !ok {
		return fmt.Errorf("%w: property snapshots unavailable", ErrOfficeInvalid)
	}
	mappings, err := properties.prepareOfficeProperties(p.Playbook.ID, p.Run.ID)
	if err != nil {
		return err
	}
	if len(mappings.CopiedFields) > 0 && !b.runs.licenseChecker.PlaybookAttributesAllowed() {
		return ErrOfficeForbidden
	}
	conditions, ok := b.runs.conditionService.(officeSnapshotConditions)
	if !ok {
		return fmt.Errorf("%w: condition snapshots unavailable", ErrOfficeInvalid)
	}
	templateConditions, err := conditions.getOfficeTemplateConditions(p.Playbook.ID)
	if err != nil {
		return err
	}
	if len(templateConditions) > 0 && !b.runs.licenseChecker.ConditionalPlaybooksAllowed() {
		return ErrOfficeForbidden
	}
	snapshot := &OfficeRunSnapshot{Version: 1, Fields: mappings.CopiedFields, Conditions: []OfficeSnapshotCondition{}}
	conditionMappings := map[string]*Condition{}
	for _, source := range templateConditions {
		if source.Version != 1 || source.ConditionExpr == nil {
			return ErrOfficeInvalid
		}
		data, err := json.Marshal(source.ConditionExpr)
		if err != nil {
			return err
		}
		var expression ConditionExprV1
		if err = json.Unmarshal(data, &expression); err != nil {
			return err
		}
		if err = expression.SwapPropertyIDs(mappings); err != nil {
			return fmt.Errorf("%w: %v", ErrOfficeInvalid, err)
		}
		if err = expression.Validate(snapshot.Fields); err != nil {
			return fmt.Errorf("%w: %v", ErrOfficeInvalid, err)
		}
		frozen := OfficeSnapshotCondition{ID: model.NewId(), Expression: expression, CreateAt: p.Run.CreateAt}
		snapshot.Conditions = append(snapshot.Conditions, frozen)
		conditionMappings[source.ID] = &Condition{ID: frozen.ID, ConditionExpr: &expression, Version: 1, PlaybookID: p.Playbook.ID, RunID: p.Run.ID, CreateAt: frozen.CreateAt, UpdateAt: frozen.CreateAt}
	}
	p.Run.PropertyFields = mappings.CopiedFields
	// The current property schema has no template default values. Office does not
	// accept initial property values, so conditions start with unset properties.
	p.Run.PropertyValues = []PropertyValue{}
	remapAssigneePropertyFieldIDs(p.Run.Checklists, mappings.FieldMappings)
	resolvePropertyUserAssignmentsFromRun(p.Run)
	for i := range p.Run.Checklists {
		for j := range p.Run.Checklists[i].Items {
			item := &p.Run.Checklists[i].Items[j]
			if item.ConditionID == "" {
				continue
			}
			condition, exists := conditionMappings[item.ConditionID]
			if !exists {
				return fmt.Errorf("%w: task refers to an unavailable condition", ErrOfficeInvalid)
			}
			item.ConditionID = condition.ID
			item.ConditionAction = ConditionActionHidden
			if condition.ConditionExpr.Evaluate(p.Run.PropertyFields, p.Run.PropertyValues) {
				item.ConditionAction = ConditionActionNone
			}
			item.ConditionReason = condition.ConditionExpr.ToString(p.Run.PropertyFields)
		}
	}
	p.Snapshot = snapshot
	return nil
}

// ensureOfficeSnapshot is called before the run is inserted and on recovery.
// It creates only missing definitions and rejects incompatible existing data.
func (b *OfficeRunBackendImpl) ensureOfficeSnapshot(p *OfficePreparedRun) error {
	if p.Snapshot == nil || p.Snapshot.Version != 1 {
		return ErrOfficeInvalid
	}
	properties, ok := b.runs.propertyService.(officeSnapshotProperties)
	if !ok {
		return ErrOfficeInvalid
	}
	mappings, err := properties.ensureOfficeProperties(p.Run.ID, p.Snapshot.Fields)
	if err != nil {
		return err
	}
	conditions, ok := b.runs.conditionService.(officeSnapshotConditions)
	if !ok {
		return ErrOfficeInvalid
	}
	resolved := make([]Condition, 0, len(p.Snapshot.Conditions))
	for _, frozen := range p.Snapshot.Conditions {
		data, err := json.Marshal(frozen.Expression)
		if err != nil {
			return err
		}
		var expression ConditionExprV1
		if err = json.Unmarshal(data, &expression); err != nil {
			return err
		}
		if err = expression.SwapPropertyIDs(mappings); err != nil {
			return fmt.Errorf("%w: %v", ErrOfficeConflict, err)
		}
		resolved = append(resolved, Condition{ID: frozen.ID, Version: 1, ConditionExpr: &expression, PlaybookID: p.Playbook.ID, RunID: p.Run.ID, CreateAt: frozen.CreateAt, UpdateAt: frozen.CreateAt})
	}
	if err = conditions.ensureOfficeConditions(p.Playbook.ID, p.Run.ID, resolved); err != nil {
		return err
	}
	remapAssigneePropertyFieldIDs(p.Run.Checklists, mappings.FieldMappings)
	p.Run.PropertyFields = mappings.CopiedFields
	return nil
}

func (s *propertyService) prepareOfficeProperties(playbookID, runID string) (*PropertyCopyResult, error) {
	fields, err := s.getAllPropertyFields(PropertyTargetTypePlaybook, playbookID)
	if err != nil {
		return nil, err
	}
	result := &PropertyCopyResult{FieldMappings: map[string]string{}, OptionMappings: map[string]string{}, CopiedFields: []PropertyField{}}
	for _, source := range fields {
		copy, err := s.copyPropertyFieldForRun(source, runID)
		if err != nil {
			return nil, err
		}
		copy.ID = model.NewId()
		copy.LinkedFieldID = nil
		copy.CreateAt, copy.UpdateAt, copy.DeleteAt = 0, 0, 0
		field, err := NewPropertyFieldFromMattermostPropertyField(copy)
		if err != nil {
			return nil, err
		}
		original, err := NewPropertyFieldFromMattermostPropertyField(source)
		if err != nil {
			return nil, err
		}
		result.FieldMappings[source.ID] = field.ID
		for i, option := range original.Attrs.Options {
			if i >= len(field.Attrs.Options) {
				return nil, ErrOfficeInvalid
			}
			result.OptionMappings[option.GetID()] = field.Attrs.Options[i].GetID()
		}
		result.CopiedFields = append(result.CopiedFields, *field)
	}
	return result, nil
}

func (s *propertyService) ensureOfficeProperties(runID string, fields []PropertyField) (*PropertyCopyResult, error) {
	existing, err := s.getOfficeRunFields(runID)
	if err != nil {
		return nil, err
	}
	result := &PropertyCopyResult{FieldMappings: map[string]string{}, OptionMappings: map[string]string{}, CopiedFields: []PropertyField{}}
	for _, frozen := range fields {
		if frozen.TargetID != runID || frozen.TargetType != PropertyTargetTypeRun || frozen.GroupID != s.groupID || frozen.Attrs.ParentID == "" {
			return nil, ErrOfficeInvalid
		}
		field, err := findOfficeField(frozen, existing)
		if err != nil {
			return nil, err
		}
		if field == nil {
			request := frozen.ToMattermostPropertyField()
			request.ID = ""
			created, createErr := s.api.Property.CreatePropertyField(request)
			if createErr != nil {
				// A host create may have committed despite a lost response.
				existing, err = s.getOfficeRunFields(runID)
				if err != nil {
					return nil, err
				}
				field, err = findOfficeField(frozen, existing)
				if err != nil {
					return nil, err
				}
				if field == nil {
					return nil, createErr
				}
			} else {
				if created == nil {
					return nil, ErrOfficeConflict
				}
				field, err = NewPropertyFieldFromMattermostPropertyField(created)
				if err != nil {
					return nil, err
				}
				if !officeFieldsMatch(frozen, *field) {
					return nil, ErrOfficeConflict
				}
				existing = append(existing, *field)
			}
		}
		result.FieldMappings[frozen.ID] = field.ID
		result.FieldMappings[field.ID] = field.ID
		for i, option := range frozen.Attrs.Options {
			result.OptionMappings[option.GetID()] = field.Attrs.Options[i].GetID()
		}
		result.CopiedFields = append(result.CopiedFields, *field)
	}
	return result, nil
}

func (s *propertyService) getOfficeRunFields(runID string) ([]PropertyField, error) {
	// Deleted definitions are evidence of an intentional edit, not permission to
	// recreate the field during an incomplete operation's recovery.
	opts := model.PropertyFieldSearchOpts{GroupID: s.groupID, TargetType: PropertyTargetTypeRun, TargetIDs: []string{runID}, IncludeDeleted: true, PerPage: PropertySearchPerPage}
	fields := []PropertyField{}
	for {
		page, err := s.api.Property.SearchPropertyFields(s.groupID, opts)
		if err != nil {
			return nil, err
		}
		for _, raw := range page {
			field, err := NewPropertyFieldFromMattermostPropertyField(raw)
			if err != nil {
				return nil, err
			}
			fields = append(fields, *field)
		}
		if len(page) < opts.PerPage {
			return fields, nil
		}
		last := page[len(page)-1]
		opts.Cursor = model.PropertyFieldSearchCursor{PropertyFieldID: last.ID, CreateAt: last.CreateAt}
	}
}

func findOfficeField(frozen PropertyField, fields []PropertyField) (*PropertyField, error) {
	var found *PropertyField
	for _, candidate := range fields {
		if candidate.Attrs.ParentID != frozen.Attrs.ParentID {
			continue
		}
		if found != nil || !officeFieldsMatch(frozen, candidate) {
			return nil, ErrOfficeConflict
		}
		copy := candidate
		found = &copy
	}
	return found, nil
}

func officeFieldsMatch(frozen, existing PropertyField) bool {
	if existing.ID == "" || existing.DeleteAt != 0 {
		return false
	}
	normalize := func(field PropertyField) []byte {
		value := field.ToMattermostPropertyField()
		value.ID, value.CreatedBy, value.UpdatedBy = "", "", ""
		value.CreateAt, value.UpdateAt = 0, 0
		data, _ := json.Marshal(value)
		return data
	}
	return bytes.Equal(normalize(frozen), normalize(existing))
}

func (s *conditionService) getOfficeTemplateConditions(playbookID string) ([]Condition, error) {
	return s.store.GetPlaybookConditions(playbookID, 0, MaxConditionsPerPlaybook)
}

func (s *conditionService) ensureOfficeConditions(playbookID, runID string, conditions []Condition) error {
	existing, err := s.store.GetRunConditions(playbookID, runID, 0, MaxConditionsPerPlaybook)
	if err != nil {
		return err
	}
	for _, frozen := range conditions {
		found, err := findOfficeCondition(frozen, existing)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		created, createErr := s.store.CreateCondition(playbookID, frozen)
		if createErr != nil {
			existing, err = s.store.GetRunConditions(playbookID, runID, 0, MaxConditionsPerPlaybook)
			if err != nil {
				return err
			}
			found, err = findOfficeCondition(frozen, existing)
			if err != nil {
				return err
			}
			if !found {
				return createErr
			}
		} else {
			if created == nil {
				return ErrOfficeConflict
			}
			found, err = findOfficeCondition(frozen, []Condition{*created})
			if err != nil {
				return err
			}
			if !found {
				return ErrOfficeConflict
			}
			existing = append(existing, *created)
		}
	}
	return nil
}

func findOfficeCondition(frozen Condition, conditions []Condition) (bool, error) {
	for _, candidate := range conditions {
		if candidate.ID != frozen.ID {
			continue
		}
		if candidate.PlaybookID != frozen.PlaybookID || candidate.RunID != frozen.RunID || candidate.Version != frozen.Version || candidate.DeleteAt != 0 {
			return false, ErrOfficeConflict
		}
		want, err := json.Marshal(frozen.ConditionExpr)
		if err != nil {
			return false, err
		}
		actual, err := json.Marshal(candidate.ConditionExpr)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(want, actual) {
			return false, ErrOfficeConflict
		}
		return true, nil
	}
	return false, nil
}
