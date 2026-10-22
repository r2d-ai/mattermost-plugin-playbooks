// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import "errors"

const OfficeCallerPlugin = "company-portal"

var (
	ErrOfficeInvalid   = errors.New("invalid Office request")
	ErrOfficeConflict  = errors.New("office operation conflicts with its saved version")
	ErrOfficeNotFound  = errors.New("office resource not found")
	ErrOfficeForbidden = errors.New("office access denied")
	ErrOfficeBlocked   = errors.New("office approval is required")
)

type OfficeRunRequest struct {
	OperationKey  string `json:"operation_key"`
	RequestID     string `json:"request_id"`
	Purpose       string `json:"purpose"`
	Generation    int64  `json:"generation"`
	PlaybookID    string `json:"playbook_id"`
	OwnerUserID   string `json:"owner_user_id"`
	TeamID        string `json:"team_id"`
	ChannelID     string `json:"channel_id"`
	Name          string `json:"name"`
	Summary       string `json:"summary"`
	SourceVersion int64  `json:"source_version"`
}

type OfficeRunResult struct {
	RunID     string `json:"run_id"`
	ChannelID string `json:"channel_id"`
	Path      string `json:"path"`
}
