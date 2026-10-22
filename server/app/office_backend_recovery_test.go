// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type officeBackendRecoveryStore struct {
	PlaybookRunStore
	run    *PlaybookRun
	setups int
}

func (s *officeBackendRecoveryStore) GetPlaybookRun(string) (*PlaybookRun, error) { return s.run, nil }
func (s *officeBackendRecoveryStore) EnsureOfficeRunSetup(*PlaybookRun, []string, []string) error {
	s.setups++
	return nil
}

func TestOfficeBackendRecoveryDeniesRevokedChannelBeforeMutations(t *testing.T) {
	for _, autoInvite := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic invitations disabled", true: "automatic invitations enabled"}[autoInvite], func(t *testing.T) {
			api := &plugintest.API{}
			client := pluginapi.NewClient(api, &plugintest.Driver{})
			actor, runID, teamID, playbookID, channelID := model.NewId(), model.NewId(), model.NewId(), model.NewId(), model.NewId()
			run := &PlaybookRun{ID: runID, TeamID: teamID, PlaybookID: playbookID, ChannelID: channelID, ReporterUserID: actor, OwnerUserID: actor, ChannelCreatedByRun: true, CreateChannelMemberOnNewParticipant: autoInvite}
			store := &officeBackendRecoveryStore{run: run}
			backend := &OfficeRunBackendImpl{runs: &PlaybookRunServiceImpl{store: store, pluginAPI: client, permissions: &PermissionsService{pluginAPI: client}}}
			api.On("GetChannel", channelID).Return(&model.Channel{Id: channelID, TeamId: teamID, Type: model.ChannelTypePrivate}, (*model.AppError)(nil)).Once()
			api.On("HasPermissionToChannel", actor, channelID, model.PermissionReadChannel).Return(false).Once()
			// An absent snapshot would fail if reconciliation ran before the ACL.
			prepared := &OfficePreparedRun{Run: run.Clone(), Playbook: &Playbook{ID: playbookID}}
			prepared.Run.ChannelID = ""
			result, err := backend.Provision(prepared)
			require.ErrorIs(t, err, ErrOfficeForbidden)
			require.Nil(t, result)
			require.Zero(t, store.setups)
			api.AssertNotCalled(t, "AddUserToChannel", mock.Anything, mock.Anything, mock.Anything)
			api.AssertNotCalled(t, "CreatePropertyField", mock.Anything)
			api.AssertNotCalled(t, "CreateChannel", mock.Anything)
			api.AssertExpectations(t)
		})
	}
}

func TestOfficeBackendRecoveryPreservesSavedSnapshotAndCurrentRun(t *testing.T) {
	api := &plugintest.API{}
	client := pluginapi.NewClient(api, &plugintest.Driver{})
	actor, runID, teamID, playbookID, channelID := model.NewId(), model.NewId(), model.NewId(), model.NewId(), model.NewId()
	groupID, logicalID, actualID, sourceID := model.NewId(), model.NewId(), model.NewId(), model.NewId()
	frozen := PropertyField{PropertyField: model.PropertyField{ID: logicalID, GroupID: groupID, TargetType: PropertyTargetTypeRun, TargetID: runID, Name: "Reviewer", Type: model.PropertyFieldTypeUser}, Attrs: Attrs{ParentID: sourceID, Visibility: PropertyFieldVisibilityWhenSet}}
	actual := frozen
	actual.ID = actualID
	itemID := model.NewId()
	run := &PlaybookRun{ID: runID, TeamID: teamID, PlaybookID: playbookID, ChannelID: channelID, ReporterUserID: actor, OwnerUserID: actor, Checklists: []Checklist{{Items: []ChecklistItem{{ID: itemID, Title: "User edited title", State: ChecklistItemStateInProgress, AssigneeType: AssigneeTypePropertyUser, AssigneePropertyFieldID: actualID}}}}}
	store := &officeBackendRecoveryStore{run: run}
	conditions := &officeSnapshotConditionStore{}
	backend := &OfficeRunBackendImpl{runs: &PlaybookRunServiceImpl{store: store, pluginAPI: client, permissions: &PermissionsService{pluginAPI: client}, propertyService: &propertyService{api: client, groupID: groupID}, conditionService: &conditionService{store: conditions}}}
	api.On("GetChannel", channelID).Return(&model.Channel{Id: channelID, TeamId: teamID, Type: model.ChannelTypePrivate}, (*model.AppError)(nil)).Once()
	api.On("HasPermissionToChannel", actor, channelID, model.PermissionReadChannel).Return(true).Once()
	api.On("SearchPropertyFields", groupID, mock.Anything).Return([]*model.PropertyField{actual.ToMattermostPropertyField()}, nil).Once()
	p := &OfficePreparedRun{Run: run.Clone(), Playbook: &Playbook{ID: playbookID}, Snapshot: &OfficeRunSnapshot{Version: 1, Fields: []PropertyField{frozen}}}
	p.Run.Checklists[0].Items[0].Title = "Original frozen title"
	p.Run.Checklists[0].Items[0].State = ChecklistItemStateOpen
	p.Run.Checklists[0].Items[0].AssigneePropertyFieldID = logicalID
	before, err := json.Marshal(p.Snapshot)
	require.NoError(t, err)
	result, err := backend.Provision(p)
	require.NoError(t, err)
	require.Equal(t, "User edited title", result.Checklists[0].Items[0].Title)
	require.Equal(t, ChecklistItemStateInProgress, result.Checklists[0].Items[0].State)
	require.Equal(t, actualID, p.Run.Checklists[0].Items[0].AssigneePropertyFieldID)
	after, err := json.Marshal(p.Snapshot)
	require.NoError(t, err)
	require.Equal(t, before, after, "host ID reconciliation must not change the saved logical snapshot")
	require.Equal(t, 1, store.setups)
	api.AssertNotCalled(t, "AddUserToChannel", mock.Anything, mock.Anything, mock.Anything)
	api.AssertNotCalled(t, "CreatePropertyField", mock.Anything)
	api.AssertExpectations(t)
}

func TestOfficeDisabledInvitationsDoNotRestoreChannelMembers(t *testing.T) {
	api := &plugintest.API{}
	service := &PlaybookRunServiceImpl{pluginAPI: pluginapi.NewClient(api, &plugintest.Driver{})}
	run := &PlaybookRun{ChannelID: model.NewId(), ReporterUserID: model.NewId(), InvitedUserIDs: []string{model.NewId()}, CreateChannelMemberOnNewParticipant: false}
	require.NoError(t, service.addOfficeInvitedChannelMembers(run))
	require.Empty(t, api.Calls)
}

func TestOfficeChannelCorrelationUsesPersistedHostFields(t *testing.T) {
	p := &OfficePreparedRun{Run: &PlaybookRun{ID: model.NewId(), TeamID: model.NewId()}, ChannelCreatorID: model.NewId()}
	channel := &model.Channel{Id: model.NewId(), TeamId: p.Run.TeamID, Type: model.ChannelTypePrivate,
		CreatorId: p.ChannelCreatorID, Name: "renamed-before-response", Header: officeChannelHeader(p.Run.ID)}
	require.Nil(t, channel.Props, "Mattermost does not persist channel props")
	require.True(t, officeChannelMatches(channel, p), "a renamed channel can be recovered by its persisted run link and creator")
	channel.CreatorId = model.NewId()
	require.False(t, officeChannelMatches(channel, p), "a user-created channel with the same header is not the reserved resource")
	channel.CreatorId = p.ChannelCreatorID
	p.CreatedChannelID = channel.Id
	channel.Header = "User edited header after binding"
	require.True(t, officeChannelMatches(channel, p), "the bound ID survives ordinary channel edits")
	channel.Id = model.NewId()
	require.False(t, officeChannelMatches(channel, p))
}

type officeChannelRecoveryStore struct {
	PlaybookRunStore
	foundID string
	boundID string
	starts  int
}

func (s *officeChannelRecoveryStore) FindOfficeChannel(string, string, string) (string, error) {
	return s.foundID, nil
}

func (s *officeChannelRecoveryStore) BindOfficeChannel(_ string, channelID string) error {
	s.boundID = channelID
	return nil
}

func (s *officeChannelRecoveryStore) StartOfficeChannelCreate(string) error {
	s.starts++
	return nil
}

func TestOfficeChannelLostResponseNeverStartsAnotherCreate(t *testing.T) {
	for _, recoverable := range []bool{true, false} {
		t.Run(map[bool]string{true: "persisted run header remains", false: "recovery metadata changed"}[recoverable], func(t *testing.T) {
			api := &plugintest.API{}
			store := &officeChannelRecoveryStore{}
			service := &PlaybookRunServiceImpl{store: store, pluginAPI: pluginapi.NewClient(api, &plugintest.Driver{})}
			p := &OfficePreparedRun{Run: &PlaybookRun{ID: model.NewId(), TeamID: model.NewId()}, ChannelCreatorID: model.NewId(), ChannelCreateStarted: true}
			if recoverable {
				store.foundID = model.NewId()
				api.On("GetChannel", store.foundID).Return(&model.Channel{Id: store.foundID, TeamId: p.Run.TeamID, Type: model.ChannelTypePrivate, CreatorId: p.ChannelCreatorID, Header: officeChannelHeader(p.Run.ID)}, (*model.AppError)(nil)).Once()
			} else {
				api.On("GetChannelByName", p.Run.TeamID, "office-"+p.Run.ID, true).Return((*model.Channel)(nil), model.NewAppError("test", "missing", nil, "", http.StatusNotFound)).Once()
			}
			channel, err := service.officeRunChannel(p)
			if recoverable {
				require.NoError(t, err)
				require.Equal(t, store.foundID, channel.Id)
				require.Equal(t, store.foundID, store.boundID)
			} else {
				require.ErrorIs(t, err, ErrOfficeConflict)
				require.Nil(t, channel)
			}
			require.Zero(t, store.starts)
			api.AssertNotCalled(t, "CreateChannel", mock.Anything)
			api.AssertExpectations(t)
		})
	}
}
