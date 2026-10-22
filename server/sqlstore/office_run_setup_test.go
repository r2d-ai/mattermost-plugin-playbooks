// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"testing"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

func TestOfficeSetupRetryPreservesMembershipChangesAndOneCreationEvent(t *testing.T) {
	db := setupTestDB(t)
	setupSQLStore(t, db)
	runs := setupPlaybookRunStore(t, db)
	run, err := runs.CreatePlaybookRun(NewBuilder(t).WithChecklists([]int{1}).ToPlaybookRun())
	require.NoError(t, err)
	initializer := runs.(app.OfficeRunInitializer)
	participant, follower := model.NewId(), model.NewId()
	require.NoError(t, initializer.EnsureOfficeRunSetup(run, []string{participant}, []string{participant, follower}))
	var counts struct {
		Participants int
		Followers    int
	}
	require.NoError(t, db.Get(&counts, `SELECT COUNT(*) FILTER (WHERE IsParticipant) AS Participants,COUNT(*) FILTER (WHERE IsFollower) AS Followers FROM IR_Run_Participants WHERE IncidentID=$1`, run.ID))
	require.Equal(t, 1, counts.Participants)
	require.Equal(t, 2, counts.Followers)
	_, err = db.Exec(`UPDATE IR_Run_Participants SET IsParticipant=false,IsFollower=false WHERE IncidentID=$1 AND UserID=$2`, run.ID, participant)
	require.NoError(t, err)
	require.NoError(t, initializer.EnsureOfficeRunSetup(run, []string{participant}, []string{participant, follower}))
	require.NoError(t, db.Get(&counts, `SELECT COUNT(*) FILTER (WHERE IsParticipant) AS Participants,COUNT(*) FILTER (WHERE IsFollower) AS Followers FROM IR_Run_Participants WHERE IncidentID=$1`, run.ID))
	require.Zero(t, counts.Participants, "retry must not restore revoked participation")
	require.Equal(t, 1, counts.Followers)
	var events int
	require.NoError(t, db.Get(&events, `SELECT COUNT(*) FROM IR_TimelineEvent WHERE IncidentID=$1 AND EventType=$2`, run.ID, string(app.PlaybookRunCreated)))
	require.Equal(t, 1, events)
}
