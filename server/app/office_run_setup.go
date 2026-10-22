// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/sirupsen/logrus"
)

type OfficeRunInitializer interface {
	EnsureOfficeRunSetup(run *PlaybookRun, participants, followers []string) error
}

func (b *OfficeRunBackendImpl) prepareOfficeAudience(p *OfficePreparedRun) error {
	s := b.runs
	users := append([]string{}, p.Run.InvitedUserIDs...)
	for _, groupID := range p.Run.InvitedGroupIDs {
		group, err := s.pluginAPI.Group.Get(groupID)
		if err != nil {
			return err
		}
		if !group.AllowReference {
			continue
		}
		for page := 0; ; page++ {
			members, err := s.pluginAPI.Group.GetMemberUsers(groupID, page, 1000)
			if err != nil {
				return err
			}
			for _, member := range members {
				users = append(users, member.Id)
			}
			if len(members) < 1000 {
				break
			}
		}
	}
	seen := map[string]bool{}
	p.Run.InvitedUserIDs = []string{}
	for _, id := range users {
		if !seen[id] && IsMemberOfTeam(id, p.Run.TeamID, s.pluginAPI) {
			seen[id] = true
			p.Run.InvitedUserIDs = append(p.Run.InvitedUserIDs, id)
		}
	}
	// Group expansion belongs to the prepared audience, not the retry time.
	p.Run.InvitedGroupIDs = nil
	var err error
	p.AutoFollowUserIDs, err = s.playbookService.GetAutoFollows(p.Playbook.ID)
	return err
}

func (b *OfficeRunBackendImpl) ensureOfficeRunSetup(run *PlaybookRun, p *OfficePreparedRun) error {
	initializer, ok := b.runs.store.(OfficeRunInitializer)
	if !ok {
		return ErrOfficeInvalid
	}
	participants := []string{run.OwnerUserID, run.ReporterUserID}
	for _, id := range p.Run.InvitedUserIDs {
		if IsMemberOfTeam(id, run.TeamID, b.runs.pluginAPI) {
			participants = append(participants, id)
		}
	}
	return initializer.EnsureOfficeRunSetup(run, participants, p.AutoFollowUserIDs)
}

func (s *PlaybookRunServiceImpl) addOfficeInvitedChannelMembers(run *PlaybookRun) error {
	if !run.CreateChannelMemberOnNewParticipant {
		return nil
	}
	if err := s.permissions.ChannelManageMembers(run.ReporterUserID, run.ChannelID); err != nil {
		return nil
	}
	for _, id := range run.InvitedUserIDs {
		user, err := s.pluginAPI.User.Get(id)
		if err != nil {
			return err
		}
		if user.DeleteAt != 0 || !IsMemberOfTeam(id, run.TeamID, s.pluginAPI) {
			continue
		}
		if _, err = s.pluginAPI.Channel.AddUser(run.ChannelID, id, run.ReporterUserID); err != nil {
			return err
		}
	}
	return nil
}

// This deterministic event identity is scoped to the timeline table. It remains
// stable across a response lost immediately after the setup transaction commits.
func OfficeRunCreatedEvent(run *PlaybookRun) *TimelineEvent {
	return &TimelineEvent{ID: run.ID, PlaybookRunID: run.ID, CreateAt: run.CreateAt, EventAt: run.CreateAt,
		EventType: PlaybookRunCreated, SubjectUserID: run.ReporterUserID, CreatorUserID: run.ReporterUserID}
}

func (s *PlaybookRunServiceImpl) dmOfficeAutoFollowers(users []string, message string, run *PlaybookRun, actor string) {
	for _, user := range users {
		if user == actor {
			continue
		}
		if s.permissions.RunView(user, run.ID) != nil || s.permissions.ChannelView(user, run.ChannelID) != nil {
			continue
		}
		if err := s.poster.DM(user, &model.Post{Message: message}); err != nil {
			logrus.WithError(err).Warn("failed to notify Office run follower")
		}
	}
}
