// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

// ReconcileOfficeGate resumes host effects after the authoritative projection
// transaction. An identical retry also repairs a previously failed unarchive.
func (s *PlaybookRunServiceImpl) ReconcileOfficeGate(runID string) error {
	run, err := s.GetPlaybookRun(runID)
	if err != nil {
		return err
	}
	if run.CurrentStatus == StatusInProgress && run.AutoArchivedChannel {
		channel, err := s.pluginAPI.Channel.Get(run.ChannelID)
		if err != nil {
			return err
		}
		if channel.DeleteAt != 0 {
			channel.DeleteAt = 0
			if err = s.pluginAPI.Channel.Update(channel); err != nil {
				return err
			}
		}
		run.AutoArchivedChannel = false
		if _, err = s.store.UpdatePlaybookRun(run); err != nil {
			return err
		}
	}
	s.sendPlaybookRunUpdatedWS(runID)
	return nil
}
