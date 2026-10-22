// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

var officeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

func (r OfficeRunRequest) Validate() error {
	if len(r.OperationKey) > 256 || !officeToken.MatchString(r.OperationKey) ||
		!validOfficeGateToken(r.RequestID) ||
		len(r.Purpose) > 64 || !officeToken.MatchString(r.Purpose) ||
		r.Generation < 1 || r.SourceVersion < 1 ||
		!model.IsValidId(r.PlaybookID) || !model.IsValidId(r.OwnerUserID) || !model.IsValidId(r.TeamID) ||
		(r.ChannelID != "" && !model.IsValidId(r.ChannelID)) ||
		len(r.Name) > MaxRunNameLength || len(r.Summary) > 65535 {
		return ErrOfficeInvalid
	}
	return nil
}

func (r OfficeRunRequest) Fingerprint() string {
	// A struct gives deterministic key order and normalizes JSON whitespace and field order.
	b, _ := json.Marshal(r)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

type OfficePreparedRun struct {
	AutoFollowUserIDs    []string           `json:"auto_follow_user_ids"`
	Run                  *PlaybookRun       `json:"run"`
	Playbook             *Playbook          `json:"playbook"`
	Public               bool               `json:"public"`
	ChannelDisplayName   string             `json:"channel_display_name"`
	ChannelCreatorID     string             `json:"channel_creator_id"`
	CreatedChannelID     string             `json:"created_channel_id,omitempty"`
	ChannelCreateStarted bool               `json:"channel_create_started,omitempty"`
	Snapshot             *OfficeRunSnapshot `json:"snapshot"`
}

type OfficeOperation struct {
	CallerPlugin string
	OperationKey string
	ActorUserID  string
	Fingerprint  string
	RequestID    string
	RunID        string
	PayloadJSON  json.RawMessage
	PreparedJSON json.RawMessage
	Completed    bool
}

type OfficeOperationStore interface {
	WithLock(caller, key string, fn func() error) error
	Claim(caller, actor string, request OfficeRunRequest) (*OfficeOperation, error)
	Get(caller, key string) (*OfficeOperation, error)
	SavePrepared(caller, key string, prepared *OfficePreparedRun) (*OfficePreparedRun, error)
	Complete(caller, key string) error
}

// OfficeRunBackend isolates the durable coordinator from host API side effects.
type OfficeRunBackend interface {
	AuthorizeNew(actor string, request OfficeRunRequest) error
	Prepare(actor, runID string, request OfficeRunRequest) (*OfficePreparedRun, error)
	AuthorizePrepared(actor string, prepared *OfficePreparedRun) error
	Provision(prepared *OfficePreparedRun) (*PlaybookRun, error)
	ReadableRun(actor, runID string) (*PlaybookRun, error)
}

type OfficeRunService struct {
	store   OfficeOperationStore
	backend OfficeRunBackend
}

func NewOfficeRunService(store OfficeOperationStore, backend OfficeRunBackend) *OfficeRunService {
	return &OfficeRunService{store: store, backend: backend}
}

func (s *OfficeRunService) Create(caller, actor string, request OfficeRunRequest) (*OfficeRunResult, error) {
	if caller != OfficeCallerPlugin || !model.IsValidId(actor) {
		return nil, ErrOfficeForbidden
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var result *OfficeRunResult
	err := s.store.WithLock(caller, request.OperationKey, func() error {
		if _, err := s.store.Get(caller, request.OperationKey); errors.Is(err, ErrOfficeNotFound) {
			if err = s.backend.AuthorizeNew(actor, request); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		op, err := s.store.Claim(caller, actor, request)
		if err != nil {
			return err
		}
		if op.ActorUserID != actor {
			return ErrOfficeNotFound
		}
		if op.Fingerprint != request.Fingerprint() {
			return ErrOfficeConflict
		}
		if op.Completed {
			result, err = s.result(actor, op.RunID)
			return err
		}
		var prepared *OfficePreparedRun
		if len(op.PreparedJSON) == 0 {
			prepared, err = s.backend.Prepare(actor, op.RunID, request)
			if err != nil {
				return err
			}
			prepared, err = s.store.SavePrepared(caller, request.OperationKey, prepared)
			if err != nil {
				return err
			}
		} else if err = json.Unmarshal(op.PreparedJSON, &prepared); err != nil {
			return err
		}
		if err = s.backend.AuthorizePrepared(actor, prepared); err != nil {
			return err
		}
		run, err := s.backend.Provision(prepared)
		if err != nil {
			return err
		}
		if run.ID != op.RunID {
			return ErrOfficeConflict
		}
		if err = s.store.Complete(caller, request.OperationKey); err != nil {
			return err
		}
		result, err = s.result(actor, op.RunID)
		return err
	})
	return result, err
}

func (s *OfficeRunService) ByKey(caller, actor, key string) (*OfficeRunResult, error) {
	if caller != OfficeCallerPlugin || !model.IsValidId(actor) {
		return nil, ErrOfficeForbidden
	}
	if len(key) > 256 || !officeToken.MatchString(key) || strings.TrimSpace(key) != key {
		return nil, ErrOfficeInvalid
	}
	op, err := s.store.Get(caller, key)
	if err != nil {
		return nil, err
	}
	if op.ActorUserID != actor || !op.Completed {
		return nil, ErrOfficeNotFound
	}
	return s.result(actor, op.RunID)
}

func (s *OfficeRunService) result(actor, runID string) (*OfficeRunResult, error) {
	run, err := s.backend.ReadableRun(actor, runID)
	if err != nil {
		return nil, err
	}
	return &OfficeRunResult{RunID: run.ID, ChannelID: run.ChannelID, Path: "/playbooks/runs/" + run.ID}, nil
}
