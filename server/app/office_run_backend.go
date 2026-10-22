// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// OfficeRunBackendImpl uses the same run creation path as ordinary runs after
// persisting a resolved snapshot. Ordinary callers cannot supply reserved IDs.
type OfficeRunBackendImpl struct{ runs *PlaybookRunServiceImpl }

func NewOfficeRunBackend(runs *PlaybookRunServiceImpl) *OfficeRunBackendImpl {
	return &OfficeRunBackendImpl{runs: runs}
}

func (b *OfficeRunBackendImpl) AuthorizeNew(actor string, request OfficeRunRequest) error {
	pb, err := b.runs.playbookService.Get(request.PlaybookID)
	if err != nil {
		return err
	}
	run := &PlaybookRun{PlaybookID: pb.ID, TeamID: request.TeamID, ChannelID: request.ChannelID, OwnerUserID: request.OwnerUserID, ReporterUserID: actor, Name: request.Name}
	if err = normalizeAndValidateRunCreationParams(run, &pb); err != nil {
		return err
	}
	if strings.TrimSpace(run.Name) == "" && run.ChannelID == "" && !TemplateLocked(&pb, pb.ChannelNameTemplate) {
		return ErrOfficeInvalid
	}
	if err = b.AuthorizePrepared(actor, &OfficePreparedRun{Run: run, Playbook: &pb, Public: pb.CreatePublicPlaybookRun}); err != nil {
		return err
	}
	return b.runs.ResolveRunCreationParams(run, &pb, nil, "office")
}

func (b *OfficeRunBackendImpl) Prepare(actor, runID string, request OfficeRunRequest) (*OfficePreparedRun, error) {
	s := b.runs
	pb, err := s.playbookService.Get(request.PlaybookID)
	if err != nil {
		return nil, err
	}
	if pb.DeleteAt != 0 {
		return nil, ErrOfficeInvalid
	}
	run := &PlaybookRun{ID: runID, PlaybookID: pb.ID, Type: RunTypePlaybook, TeamID: request.TeamID, ChannelID: request.ChannelID,
		OwnerUserID: request.OwnerUserID, ReporterUserID: actor, Name: request.Name, Summary: request.Summary}
	if err = normalizeAndValidateRunCreationParams(run, &pb); err != nil {
		return nil, err
	}
	if strings.TrimSpace(run.Name) == "" && run.ChannelID == "" && !TemplateLocked(&pb, pb.ChannelNameTemplate) {
		return nil, ErrOfficeInvalid
	}
	run.SetChecklistFromPlaybook(pb)
	run.SetConfigurationFromPlaybook(pb, "office")
	// Office names the intended owner explicitly; do not silently replace that
	// identity with a playbook default.
	run.DefaultOwnerID = ""
	prepared := &OfficePreparedRun{Run: run, Playbook: &pb, Public: pb.CreatePublicPlaybookRun, ChannelCreatorID: s.configService.GetConfiguration().BotUserID}
	if err = b.AuthorizePrepared(actor, prepared); err != nil {
		return nil, err
	}
	if err = s.ResolveRunCreationParams(run, &pb, nil, "office"); err != nil {
		return nil, err
	}
	displayName, err := s.resolveAndAllocate(run, &pb, nil, "office")
	if err != nil {
		return nil, err
	}
	prepared.ChannelDisplayName = displayName
	run.CreateAt = model.GetMillis()
	run.LastStatusUpdateAt = run.CreateAt
	run.CurrentStatus = StatusInProgress
	for i := range run.Checklists {
		run.Checklists[i].ID = model.NewId()
		for j := range run.Checklists[i].Items {
			run.Checklists[i].Items[j].ID = model.NewId()
		}
	}
	resolveRoleAssignments(run.Checklists, run.OwnerUserID, actor)
	if err = b.prepareOfficeSnapshot(prepared); err != nil {
		return nil, err
	}
	if err = b.prepareOfficeAudience(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (b *OfficeRunBackendImpl) AuthorizePrepared(actor string, p *OfficePreparedRun) error {
	s := b.runs
	user, err := s.pluginAPI.User.Get(actor)
	if err != nil || user.DeleteAt != 0 || user.IsBot {
		return ErrOfficeForbidden
	}
	// Use the current playbook's membership policy on every attempt, while
	// retaining the prepared content and audience for recovery.
	current, err := s.playbookService.Get(p.Playbook.ID)
	if err != nil {
		return err
	}
	if current.DeleteAt != 0 {
		return ErrOfficeForbidden
	}
	if err = s.permissions.RunCreate(actor, current, p.Run.TeamID); err != nil {
		return err
	}
	owner, err := s.pluginAPI.User.Get(p.Run.OwnerUserID)
	if err != nil || owner.DeleteAt != 0 || !IsMemberOfTeam(owner.Id, p.Run.TeamID, s.pluginAPI) {
		return ErrOfficeForbidden
	}
	if p.Run.ChannelID == "" {
		permission := model.PermissionCreatePrivateChannel
		if p.Public {
			permission = model.PermissionCreatePublicChannel
		}
		if !s.pluginAPI.User.HasPermissionToTeam(actor, p.Run.TeamID, permission) {
			return ErrOfficeForbidden
		}
		return nil
	}
	channel, err := s.pluginAPI.Channel.Get(p.Run.ChannelID)
	if err != nil {
		return err
	}
	if channel.DeleteAt != 0 || channel.IsGroupOrDirect() || channel.TeamId != p.Run.TeamID {
		return ErrOfficeInvalid
	}
	if err = s.permissions.ChannelView(actor, channel.Id); err != nil {
		return err
	}
	permission := model.PermissionManagePublicChannelProperties
	if channel.Type == model.ChannelTypePrivate {
		permission = model.PermissionManagePrivateChannelProperties
	}
	if !s.pluginAPI.User.HasPermissionToChannel(actor, channel.Id, permission) {
		return ErrOfficeForbidden
	}
	if _, err = s.pluginAPI.Channel.GetMember(channel.Id, owner.Id); err != nil {
		if !errors.Is(err, pluginapi.ErrNotFound) {
			return err
		}
		return s.permissions.ChannelManageMembers(actor, channel.Id)
	}
	return nil
}

func (b *OfficeRunBackendImpl) ReadableRun(actor, runID string) (*PlaybookRun, error) {
	s := b.runs
	run, err := s.GetPlaybookRun(runID)
	if err != nil {
		return nil, err
	}
	if run.DeleteAt != 0 {
		return nil, ErrOfficeNotFound
	}
	if err = s.permissions.RunView(actor, runID); err != nil {
		return nil, ErrOfficeNotFound
	}
	if err = s.permissions.ChannelView(actor, run.ChannelID); err != nil {
		return nil, ErrOfficeNotFound
	}
	return run, nil
}

func (b *OfficeRunBackendImpl) Provision(p *OfficePreparedRun) (*PlaybookRun, error) {
	s := b.runs
	// A prior attempt may have committed the run then lost its response. Resume
	// core memberships without replaying user messages or external webhooks.
	if existing, err := s.store.GetPlaybookRun(p.Run.ID); err == nil {
		if existing.DeleteAt != 0 || existing.TeamID != p.Run.TeamID || existing.PlaybookID != p.Run.PlaybookID || existing.ReporterUserID != p.Run.ReporterUserID {
			return nil, ErrOfficeConflict
		}
		channel, channelErr := s.pluginAPI.Channel.Get(existing.ChannelID)
		if channelErr != nil {
			return nil, channelErr
		}
		if channel.DeleteAt != 0 {
			return nil, ErrOfficeConflict
		}
		// Initial channel membership is established before the run row is
		// inserted. Missing access here is revocation, not unfinished setup.
		if err = s.permissions.ChannelView(p.Run.ReporterUserID, channel.Id); err != nil {
			return nil, ErrOfficeForbidden
		}
		if p.Run.ChannelID == "" && !officeChannelMatches(channel, p) {
			return nil, ErrOfficeConflict
		}
		if p.Run.ChannelID != "" && existing.ChannelID != p.Run.ChannelID {
			return nil, ErrOfficeConflict
		}
		if err = b.ensureOfficeSnapshot(p); err != nil {
			return nil, err
		}
		if err = b.ensureOfficeRunSetup(existing, p); err != nil {
			return nil, err
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := b.ensureOfficeSnapshot(p); err != nil {
		return nil, err
	}
	run := p.Run.Clone()
	return s.createPlaybookRun(run, p.Playbook, run.ReporterUserID, p.Public, "office", nil, p)
}

func officeChannelMatches(channel *model.Channel, p *OfficePreparedRun) bool {
	channelType := model.ChannelTypePrivate
	if p.Public {
		channelType = model.ChannelTypeOpen
	}
	if channel.DeleteAt != 0 || channel.TeamId != p.Run.TeamID || channel.Type != channelType ||
		p.ChannelCreatorID == "" || channel.CreatorId != p.ChannelCreatorID {
		return false
	}
	if p.CreatedChannelID != "" {
		return channel.Id == p.CreatedChannelID
	}
	return channel.Header == officeChannelHeader(p.Run.ID)
}

func officeChannelHeader(runID string) string {
	return fmt.Sprintf("Playbook run: %s", GetRunDetailsRelativeURL(runID))
}

func (s *PlaybookRunServiceImpl) officeRunChannel(p *OfficePreparedRun) (*model.Channel, error) {
	recovery, ok := s.store.(OfficeChannelRecovery)
	if !ok {
		return nil, ErrOfficeInvalid
	}
	bind := func(channel *model.Channel) (*model.Channel, error) {
		if !officeChannelMatches(channel, p) {
			return nil, ErrOfficeConflict
		}
		if err := recovery.BindOfficeChannel(p.Run.ID, channel.Id); err != nil {
			return nil, err
		}
		p.CreatedChannelID = channel.Id
		return channel, nil
	}
	if p.CreatedChannelID != "" {
		channel, err := s.pluginAPI.Channel.Get(p.CreatedChannelID)
		if err != nil {
			return nil, err
		}
		return bind(channel)
	}
	// The response may have been lost before binding the host ID. Search the
	// persisted run link and creator too, since channel names can be changed.
	// Channel.Props is presentation data and is not persisted by Mattermost.
	channelID, err := recovery.FindOfficeChannel(p.Run.TeamID, p.ChannelCreatorID, officeChannelHeader(p.Run.ID))
	if err != nil {
		return nil, err
	}
	if channelID != "" {
		channel, err := s.pluginAPI.Channel.Get(channelID)
		if err != nil {
			return nil, err
		}
		return bind(channel)
	}
	name := "office-" + p.Run.ID
	existing, err := s.pluginAPI.Channel.GetByName(p.Run.TeamID, name, true)
	if err == nil {
		if !officeChannelMatches(existing, p) {
			return nil, ErrOfficeConflict
		}
		return bind(existing)
	}
	if !errors.Is(err, pluginapi.ErrNotFound) {
		return nil, err
	}
	// The host has no idempotent create primitive. Once attempted, an absent
	// match may mean that an administrator changed both recovery fields. Never
	// create another channel in that ambiguous state, including after a crash
	// immediately before the host call. An operator must reconcile it.
	if p.ChannelCreateStarted {
		return nil, ErrOfficeConflict
	}
	if err = recovery.StartOfficeChannelCreate(p.Run.ID); err != nil {
		return nil, err
	}
	p.ChannelCreateStarted = true
	displayName := p.ChannelDisplayName
	if displayName == "" {
		displayName = p.Run.Name
	}
	channelType := model.ChannelTypePrivate
	if p.Public {
		channelType = model.ChannelTypeOpen
	}
	channel := &model.Channel{TeamId: p.Run.TeamID, Type: channelType, Name: name,
		DisplayName: truncateRunes(displayName, model.ChannelDisplayNameMaxRunes),
		Header:      officeChannelHeader(p.Run.ID),
		CreatorId:   p.ChannelCreatorID}
	if err = s.pluginAPI.Channel.Create(channel); err != nil {
		// Creation can succeed remotely while the response is lost. Reconcile
		// only the reserved resource; never create a random suffix channel.
		existing, lookupErr := s.pluginAPI.Channel.GetByName(p.Run.TeamID, name, true)
		if lookupErr == nil && officeChannelMatches(existing, p) {
			return bind(existing)
		}
		logrus.WithError(err).Warn("Office channel creation requires retry")
		return nil, err
	}
	return bind(channel)
}

type OfficeChannelRecovery interface {
	FindOfficeChannel(teamID, creatorID, header string) (string, error)
	BindOfficeChannel(runID, channelID string) error
	StartOfficeChannelCreate(runID string) error
}
