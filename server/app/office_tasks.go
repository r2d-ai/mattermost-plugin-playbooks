// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

const OfficeTaskCursorMaxLength = 2048

// OfficeTaskRunService supplies all candidate runs without applying pagination.
type OfficeTaskRunService interface {
	GetRunsWithAssignedTasks(userID string) ([]AssignedRun, error)
	GetPlaybookRun(runID string) (*PlaybookRun, error)
}

type OfficeReadPermissions interface {
	RunView(userID, runID string) error
	ChannelView(userID, channelID string) error
}

type OfficeTask struct {
	ID      string `json:"id"`
	RunID   string `json:"run_id"`
	RunName string `json:"run_name"`
	Title   string `json:"title"`
	State   string `json:"state"`
	DueAt   *int64 `json:"due_at"`
	Path    string `json:"path"`
}

type OfficeTaskPage struct {
	Items      []OfficeTask `json:"items"`
	NextCursor string       `json:"next_cursor"`
}

type OfficeTaskService struct {
	runs        OfficeTaskRunService
	permissions OfficeReadPermissions
}

func NewOfficeTaskService(runs OfficeTaskRunService, permissions OfficeReadPermissions) *OfficeTaskService {
	return &OfficeTaskService{runs: runs, permissions: permissions}
}

// officeTaskCursor identifies the last emitted task, keeping pagination stable
// when an earlier task is completed or the actor loses access to its run.
type officeTaskCursor struct {
	Version int    `json:"v"`
	Actor   string `json:"actor"`
	DueAt   int64  `json:"due"`
	ID      string `json:"id"`
	RunID   string `json:"run"`
}

func decodeOfficeTaskCursor(actor, encoded string) (*officeTaskCursor, error) {
	if len(encoded) > OfficeTaskCursorMaxLength {
		return nil, ErrOfficeInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, ErrOfficeInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var cursor officeTaskCursor
	if err = decoder.Decode(&cursor); err != nil {
		return nil, ErrOfficeInvalid
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrOfficeInvalid
	}
	if cursor.Version != 1 || cursor.Actor != actor || cursor.DueAt < 0 || cursor.ID == "" || cursor.RunID == "" || len(cursor.ID) > 128 || len(cursor.RunID) > 128 {
		return nil, ErrOfficeInvalid
	}
	return &cursor, nil
}

func officeTaskPosition(actor string, task OfficeTask) officeTaskCursor {
	position := officeTaskCursor{Version: 1, Actor: actor, ID: task.ID, RunID: task.RunID}
	if task.DueAt != nil {
		position.DueAt = *task.DueAt
	}
	return position
}

func officeTaskBefore(a, b officeTaskCursor) bool {
	// Undated tasks follow every dated task.
	if a.DueAt == 0 && b.DueAt != 0 {
		return false
	}
	if a.DueAt != 0 && b.DueAt == 0 {
		return true
	}
	if a.DueAt != b.DueAt {
		return a.DueAt < b.DueAt
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.RunID < b.RunID
}

func (s *OfficeTaskService) List(actor string, limit int, encodedCursor string) (*OfficeTaskPage, error) {
	if actor == "" || limit < 1 || limit > 100 {
		return nil, ErrOfficeInvalid
	}
	var cursor *officeTaskCursor
	if encodedCursor != "" {
		var err error
		cursor, err = decodeOfficeTaskCursor(actor, encodedCursor)
		if err != nil {
			return nil, err
		}
	}
	candidates, err := s.runs.GetRunsWithAssignedTasks(actor)
	if err != nil {
		return nil, err
	}
	items := []OfficeTask{}
	seenRuns := map[string]bool{}
	for _, candidate := range candidates {
		if seenRuns[candidate.PlaybookRunID] {
			continue
		}
		seenRuns[candidate.PlaybookRunID] = true
		run, err := s.runs.GetPlaybookRun(candidate.PlaybookRunID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if run == nil || run.CurrentStatus != StatusInProgress || run.DeleteAt != 0 || run.ChannelID == "" {
			continue
		}
		if err = s.permissions.RunView(actor, run.ID); err != nil {
			if errors.Is(err, ErrNoPermissions) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if err = s.permissions.ChannelView(actor, run.ChannelID); err != nil {
			if errors.Is(err, ErrNoPermissions) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		for _, checklist := range run.Checklists {
			for _, item := range checklist.Items {
				if item.ID == "" || item.AssigneeID != actor || (item.State != ChecklistItemStateOpen && item.State != ChecklistItemStateInProgress) || item.ConditionAction == ConditionActionHidden {
					continue
				}
				task := OfficeTask{ID: item.ID, RunID: run.ID, RunName: run.Name, Title: item.Title, State: "open", Path: "/playbooks/runs/" + run.ID}
				if item.State == ChecklistItemStateInProgress {
					task.State = "in_progress"
				}
				if item.DueDate > 0 {
					due := item.DueDate
					task.DueAt = &due
				}
				if cursor != nil && !officeTaskBefore(*cursor, officeTaskPosition(actor, task)) {
					continue
				}
				items = append(items, task)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return officeTaskBefore(officeTaskPosition(actor, items[i]), officeTaskPosition(actor, items[j]))
	})
	page := &OfficeTaskPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		data, err := json.Marshal(officeTaskPosition(actor, page.Items[limit-1]))
		if err != nil {
			return nil, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}
