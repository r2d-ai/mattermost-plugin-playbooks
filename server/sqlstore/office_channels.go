// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/mattermost/mattermost/server/public/model"
)

func (s *playbookRunStore) FindOfficeChannel(teamID, creatorID, header string) (string, error) {
	var ids []string
	if err := s.store.db.Select(&ids, `SELECT ID FROM Channels WHERE TeamID=$1 AND CreatorID=$2 AND Header=$3 LIMIT 2`, teamID, creatorID, header); err != nil {
		return "", err
	}
	if len(ids) > 1 {
		return "", app.ErrOfficeConflict
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

func (s *playbookRunStore) BindOfficeChannel(runID, channelID string) error {
	result, err := s.store.db.Exec(`UPDATE IR_OfficeOperation SET PreparedJSON=jsonb_set(PreparedJSON,'{created_channel_id}',to_jsonb($2::text)),UpdateAt=$3
		WHERE RunID=$1 AND PreparedJSON IS NOT NULL AND COALESCE(PreparedJSON->>'created_channel_id','') IN ('',$2)`, runID, channelID, model.GetMillis())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return app.ErrOfficeConflict
	}
	return err
}

func (s *playbookRunStore) StartOfficeChannelCreate(runID string) error {
	result, err := s.store.db.Exec(`UPDATE IR_OfficeOperation SET PreparedJSON=jsonb_set(PreparedJSON,'{channel_create_started}','true'::jsonb),UpdateAt=$2
		WHERE RunID=$1 AND PreparedJSON IS NOT NULL AND COALESCE(PreparedJSON->>'channel_create_started','false')='false'`, runID, model.GetMillis())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return app.ErrOfficeConflict
	}
	return err
}
