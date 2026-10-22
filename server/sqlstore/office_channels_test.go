// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

func TestOfficeChannelRecoverySurvivesRenameAndBindsOnlyOnce(t *testing.T) {
	db := setupTestDB(t)
	store := setupSQLStore(t, db)
	recovery := setupPlaybookRunStore(t, db).(app.OfficeChannelRecovery)
	teamID, channelID, creatorID := model.NewId(), model.NewId(), model.NewId()
	header := "Playbook run: /playbooks/runs/" + model.NewId()
	id, err := recovery.FindOfficeChannel(teamID, creatorID, header)
	require.NoError(t, err)
	require.Empty(t, id)
	_, err = db.Exec(`INSERT INTO Channels (ID,TeamID,Name,CreatorID,Header) VALUES ($1,$2,'renamed-before-retry',$3,$4)`, channelID, teamID, creatorID, header)
	require.NoError(t, err)
	id, err = recovery.FindOfficeChannel(teamID, creatorID, header)
	require.NoError(t, err)
	require.Equal(t, channelID, id)
	operations := NewOfficeOperationStore(store)
	op, err := operations.Claim(app.OfficeCallerPlugin, model.NewId(), app.OfficeRunRequest{OperationKey: "channel-recovery", RequestID: "request-1"})
	require.NoError(t, err)
	_, err = operations.SavePrepared(op.CallerPlugin, op.OperationKey, &app.OfficePreparedRun{Run: &app.PlaybookRun{ID: op.RunID}, ChannelCreatorID: creatorID})
	require.NoError(t, err)
	require.NoError(t, recovery.StartOfficeChannelCreate(op.RunID))
	require.ErrorIs(t, recovery.StartOfficeChannelCreate(op.RunID), app.ErrOfficeConflict, "even a second worker may not initiate another host creation")
	require.NoError(t, recovery.BindOfficeChannel(op.RunID, channelID))
	require.NoError(t, recovery.BindOfficeChannel(op.RunID, channelID))
	require.ErrorIs(t, recovery.BindOfficeChannel(op.RunID, model.NewId()), app.ErrOfficeConflict)
	op, err = NewOfficeOperationStore(store).Get(op.CallerPlugin, op.OperationKey)
	require.NoError(t, err)
	var saved app.OfficePreparedRun
	require.NoError(t, json.Unmarshal(op.PreparedJSON, &saved))
	require.Equal(t, channelID, saved.CreatedChannelID)
	require.True(t, saved.ChannelCreateStarted)
	_, err = db.Exec(`INSERT INTO Channels (ID,TeamID,Name,CreatorID,Header) VALUES ($1,$2,'duplicate-correlation',$3,$4)`, model.NewId(), teamID, creatorID, header)
	require.NoError(t, err)
	_, err = recovery.FindOfficeChannel(teamID, creatorID, header)
	require.ErrorIs(t, err, app.ErrOfficeConflict, "ambiguous correlation must fail closed, never create another channel")
}
