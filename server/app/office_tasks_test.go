// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type officeTaskSourceStub struct {
	candidates []AssignedRun
	runs       map[string]*PlaybookRun
	err        error
	actor      string
}

func (s *officeTaskSourceStub) GetRunsWithAssignedTasks(actor string) ([]AssignedRun, error) {
	s.actor = actor
	return s.candidates, s.err
}

func (s *officeTaskSourceStub) GetPlaybookRun(id string) (*PlaybookRun, error) {
	run := s.runs[id]
	if run == nil {
		return nil, ErrNotFound
	}
	return run, nil
}

type officeTaskPermissionsStub struct {
	deniedRuns     map[string]bool
	deniedChannels map[string]bool
}

func (s officeTaskPermissionsStub) RunView(_, id string) error {
	if s.deniedRuns[id] {
		return ErrNoPermissions
	}
	return nil
}

func (s officeTaskPermissionsStub) ChannelView(_, id string) error {
	if s.deniedChannels[id] {
		return ErrNoPermissions
	}
	return nil
}

func TestOfficeTasksFilterBeforePagination(t *testing.T) {
	actor := "actor"
	task := func(id string, due int64) ChecklistItem {
		return ChecklistItem{ID: id, Title: id, AssigneeID: actor, DueDate: due}
	}
	hidden := task("hidden", 1)
	hidden.ConditionAction = ConditionActionHidden
	closed := task("closed", 2)
	closed.State = "closed"
	skipped := task("skipped", 3)
	skipped.State = "skipped"
	other := task("other", 4)
	other.AssigneeID = "other"
	runs := map[string]*PlaybookRun{}
	candidates := []AssignedRun{}
	addRun := func(id, status string, items ...ChecklistItem) {
		runs[id] = &PlaybookRun{ID: id, Name: "Run " + id, ChannelID: "channel" + id, CurrentStatus: status, Checklists: []Checklist{{ID: "checklist", Items: items}}}
		candidates = append(candidates, AssignedRun{RunLink: RunLink{PlaybookRunID: id}})
	}
	addRun("norun", StatusInProgress, task("norun-task", 1))
	addRun("nochannel", StatusInProgress, task("nochannel-task", 1))
	addRun("finished", StatusFinished, task("finished-task", 1))
	addRun("a", StatusInProgress, hidden, closed, skipped, other, task("z", 0), task("b", 100), task("a", 100))
	addRun("b", StatusInProgress, task("c", 200))
	runs["b"].Checklists[0].Items[0].State = ChecklistItemStateInProgress
	source := &officeTaskSourceStub{candidates: candidates, runs: runs}
	svc := NewOfficeTaskService(source, officeTaskPermissionsStub{deniedRuns: map[string]bool{"norun": true}, deniedChannels: map[string]bool{"channelnochannel": true}})
	first, err := svc.List(actor, 2, "")
	require.NoError(t, err)
	require.Equal(t, actor, source.actor)
	require.Len(t, first.Items, 2)
	require.Equal(t, []string{"a", "b"}, []string{first.Items[0].ID, first.Items[1].ID})
	require.Equal(t, "open", first.Items[0].State)
	require.Equal(t, int64(100), *first.Items[0].DueAt)
	require.Equal(t, "/playbooks/runs/a", first.Items[0].Path)
	require.NotEmpty(t, first.NextCursor)
	second, err := svc.List(actor, 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	require.Equal(t, []string{"c", "z"}, []string{second.Items[0].ID, second.Items[1].ID})
	require.Equal(t, "in_progress", second.Items[0].State)
	require.Nil(t, second.Items[1].DueAt)
	require.Empty(t, second.NextCursor)
	_, err = svc.List("other", 2, first.NextCursor)
	require.ErrorIs(t, err, ErrOfficeInvalid)
}

func TestOfficeTasksCursorAndLimitValidation(t *testing.T) {
	source := &officeTaskSourceStub{}
	svc := NewOfficeTaskService(source, officeTaskPermissionsStub{})
	for _, cursor := range []string{"!", strings.Repeat("a", 2049), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"actor":"actor"}`))} {
		_, err := svc.List("actor", 20, cursor)
		require.ErrorIs(t, err, ErrOfficeInvalid)
	}
	for _, limit := range []int{0, -1, 101} {
		_, err := svc.List("actor", limit, "")
		require.ErrorIs(t, err, ErrOfficeInvalid)
	}
	result, err := svc.List("actor", 20, "")
	require.NoError(t, err)
	require.NotNil(t, result.Items)
	require.Empty(t, result.Items)
	source.err = errors.New("database unavailable")
	_, err = svc.List("actor", 20, "")
	require.ErrorContains(t, err, "database unavailable")
}

func TestOfficeTaskCursorSurvivesEarlierCompletionAndPermissionChange(t *testing.T) {
	source := &officeTaskSourceStub{runs: map[string]*PlaybookRun{}}
	permissions := officeTaskPermissionsStub{deniedChannels: map[string]bool{}}
	for _, id := range []string{"a", "b", "c", "d"} {
		source.candidates = append(source.candidates, AssignedRun{RunLink: RunLink{PlaybookRunID: id}})
		source.runs[id] = &PlaybookRun{ID: id, ChannelID: id, CurrentStatus: StatusInProgress, Checklists: []Checklist{{Items: []ChecklistItem{{ID: id, AssigneeID: "actor", DueDate: 100}}}}}
	}
	svc := NewOfficeTaskService(source, permissions)
	first, err := svc.List("actor", 1, "")
	require.NoError(t, err)
	require.Equal(t, "a", first.Items[0].ID)
	source.runs["a"].Checklists[0].Items[0].State = ChecklistItemStateClosed
	permissions.deniedChannels["b"] = true
	second, err := svc.List("actor", 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	require.Equal(t, []string{"c", "d"}, []string{second.Items[0].ID, second.Items[1].ID})
	require.Empty(t, second.NextCursor)
}
