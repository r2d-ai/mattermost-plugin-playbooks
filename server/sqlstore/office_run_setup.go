// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import "github.com/mattermost/mattermost-plugin-playbooks/server/app"

func (s *playbookRunStore) EnsureOfficeRunSetup(run *app.PlaybookRun, participants, followers []string) error {
	tx, err := s.store.db.Beginx()
	if err != nil {
		return err
	}
	defer s.store.finalizeTransaction(tx)
	if _, err = lockOfficeGateRun(tx, run.ID); err != nil {
		return err
	}
	members := map[string]bool{}
	for _, id := range followers {
		members[id] = false
	}
	for _, id := range participants {
		members[id] = true
	}
	for id, participant := range members {
		_, err = tx.Exec(`INSERT INTO IR_Run_Participants (IncidentID,UserID,IsParticipant,IsFollower)
			VALUES($1,$2,$3,true) ON CONFLICT (IncidentID,UserID) DO NOTHING`, run.ID, id, participant)
		if err != nil {
			return err
		}
	}
	event := app.OfficeRunCreatedEvent(run)
	_, err = tx.Exec(`INSERT INTO IR_TimelineEvent (ID,IncidentID,CreateAt,DeleteAt,EventAt,EventType,Summary,Details,PostID,SubjectUserID,CreatorUserID)
		VALUES($1,$2,$3,0,$3,$4,'','','',$5,$5) ON CONFLICT (ID) DO NOTHING`, event.ID, run.ID, event.CreateAt, string(event.EventType), run.ReporterUserID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
