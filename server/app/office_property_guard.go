// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import "slices"

// Check before the host property write so a known blocked task cannot be
// reassigned or hidden through a property side effect. The final checklist SQL
// write independently checks the current projection under the run row lock.
func (s *PlaybookRunServiceImpl) checkOfficePropertyChange(actor string, run *PlaybookRun, fieldID, parentID string) error {
	if s.officeGates == nil {
		return nil
	}
	gates, err := s.officeGates.List(run.ID)
	if err != nil {
		return err
	}
	protected := map[string]bool{}
	for _, gate := range gates {
		if gate.Projection.Status != "approved" {
			for _, id := range gate.Projection.DependentTaskIDs {
				protected[id] = true
			}
		}
	}
	if len(protected) == 0 {
		return nil
	}
	conditionIDs := map[string]bool{}
	for _, checklist := range run.Checklists {
		for _, item := range checklist.Items {
			if !protected[item.ID] {
				continue
			}
			if item.AssigneeType == AssigneeTypePropertyUser && (item.AssigneePropertyFieldID == fieldID || (parentID != "" && item.AssigneePropertyFieldID == parentID)) {
				return ErrOfficeBlocked
			}
			if item.ConditionID != "" {
				conditionIDs[item.ConditionID] = true
			}
		}
	}
	if len(conditionIDs) == 0 {
		return nil
	}
	conditions, err := s.conditionService.GetRunConditions(actor, run.PlaybookID, run.ID, 0, MaxConditionsPerPlaybook)
	if err != nil {
		return err
	}
	for _, condition := range conditions.Items {
		if conditionIDs[condition.ID] && condition.ConditionExpr != nil {
			fields, _ := condition.ConditionExpr.ExtractPropertyIDs()
			if slices.Contains(fields, fieldID) || (parentID != "" && slices.Contains(fields, parentID)) {
				return ErrOfficeBlocked
			}
		}
	}
	return nil
}
