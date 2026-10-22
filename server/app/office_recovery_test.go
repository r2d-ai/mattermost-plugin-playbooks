// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

var errOfficeRecoveryCrash = errors.New("injected process interruption")

// The store outlives service instances and returns serialized copies, as the
// database does. Faults happen after durable writes, when the response is lost.
type officeRecoveryStore struct {
	operationLock sync.Mutex
	mu            sync.Mutex
	operations    map[string]OfficeOperation
	fault         string
}

func (s *officeRecoveryStore) WithLock(_, _ string, fn func() error) error {
	s.operationLock.Lock()
	defer s.operationLock.Unlock()
	return fn()
}
func (s *officeRecoveryStore) Claim(caller, actor string, request OfficeRunRequest) (*OfficeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := caller + "\x00" + request.OperationKey
	op, found := s.operations[key]
	if !found {
		payload, _ := json.Marshal(request)
		op = OfficeOperation{CallerPlugin: caller, OperationKey: request.OperationKey, ActorUserID: actor, Fingerprint: request.Fingerprint(), RequestID: request.RequestID, RunID: model.NewId(), PayloadJSON: payload}
		s.operations[key] = op
	}
	return &op, nil
}
func (s *officeRecoveryStore) Get(caller, key string) (*OfficeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, found := s.operations[caller+"\x00"+key]
	if !found {
		return nil, ErrOfficeNotFound
	}
	return &op, nil
}
func (s *officeRecoveryStore) SavePrepared(caller, key string, p *OfficePreparedRun) (*OfficePreparedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := caller + "\x00" + key
	op := s.operations[index]
	if len(op.PreparedJSON) == 0 {
		data, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		op.PreparedJSON = data
		s.operations[index] = op
	}
	if s.fault == "after preparation" {
		s.fault = ""
		return nil, errOfficeRecoveryCrash
	}
	var saved OfficePreparedRun
	err := json.Unmarshal(op.PreparedJSON, &saved)
	return &saved, err
}
func (s *officeRecoveryStore) Complete(caller, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := caller + "\x00" + key
	op := s.operations[index]
	op.Completed = true
	s.operations[index] = op
	if s.fault == "after completion" {
		s.fault = ""
		return errOfficeRecoveryCrash
	}
	return nil
}

type officeRecoveryState struct {
	mu                       sync.Mutex
	channels                 map[string]string
	runs                     map[string]*PlaybookRun
	templateTitle            string
	fault                    string
	preparations, provisions int
	denyCreate, denyRead     bool
}
type officeRecoveryBackend struct{ state *officeRecoveryState }

func (b *officeRecoveryBackend) AuthorizeNew(actor string, _ OfficeRunRequest) error {
	return b.AuthorizePrepared(actor, nil)
}

func TestOfficeUnauthorizedCreateDoesNotReserveKey(t *testing.T) {
	store, state := newOfficeRecoveryFixture()
	state.denyCreate = true
	service := NewOfficeRunService(store, &officeRecoveryBackend{state})
	request := validOfficeRunRequest()
	_, err := service.Create(OfficeCallerPlugin, model.NewId(), request)
	require.ErrorIs(t, err, ErrOfficeForbidden)
	_, err = store.Get(OfficeCallerPlugin, request.OperationKey)
	require.ErrorIs(t, err, ErrOfficeNotFound)
	state.denyCreate = false
	_, err = service.Create(OfficeCallerPlugin, model.NewId(), request)
	require.NoError(t, err)
}

func (b *officeRecoveryBackend) Prepare(actor, runID string, r OfficeRunRequest) (*OfficePreparedRun, error) {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preparations++
	return &OfficePreparedRun{Run: &PlaybookRun{ID: runID, Name: r.Name, Summary: r.Summary, TeamID: r.TeamID, PlaybookID: r.PlaybookID, OwnerUserID: r.OwnerUserID, ReporterUserID: actor, Checklists: []Checklist{{ID: model.NewId(), Items: []ChecklistItem{{ID: model.NewId(), Title: s.templateTitle, AssigneeID: r.OwnerUserID}}}}}, Playbook: &Playbook{ID: r.PlaybookID}, ChannelCreatorID: model.NewId()}, nil
}
func (b *officeRecoveryBackend) AuthorizePrepared(string, *OfficePreparedRun) error {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.denyCreate {
		return ErrOfficeForbidden
	}
	return nil
}
func (b *officeRecoveryBackend) Provision(p *OfficePreparedRun) (*PlaybookRun, error) {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provisions++
	if s.channels[p.Run.ID] == "" {
		s.channels[p.Run.ID] = model.NewId()
	}
	if s.fault == "after channel" {
		s.fault = ""
		return nil, errOfficeRecoveryCrash
	}
	if s.runs[p.Run.ID] == nil {
		run := p.Run.Clone()
		run.ChannelID = s.channels[run.ID]
		s.runs[run.ID] = run
	}
	if s.fault == "after run" {
		s.fault = ""
		return nil, errOfficeRecoveryCrash
	}
	return s.runs[p.Run.ID].Clone(), nil
}
func (b *officeRecoveryBackend) ReadableRun(_, runID string) (*PlaybookRun, error) {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.denyRead || s.runs[runID] == nil {
		return nil, ErrOfficeNotFound
	}
	return s.runs[runID].Clone(), nil
}

func newOfficeRecoveryFixture() (*officeRecoveryStore, *officeRecoveryState) {
	return &officeRecoveryStore{operations: map[string]OfficeOperation{}}, &officeRecoveryState{channels: map[string]string{}, runs: map[string]*PlaybookRun{}, templateTitle: "Original checklist"}
}

func TestOfficeRunRecoveryAcrossDurableBoundaries(t *testing.T) {
	for _, fault := range []string{"after preparation", "after channel", "after run", "after completion"} {
		t.Run(fault, func(t *testing.T) {
			store, state := newOfficeRecoveryFixture()
			store.fault, state.fault = fault, fault
			actor := model.NewId()
			request := validOfficeRunRequest()
			request.Summary = "Approved delivery scope"
			first := NewOfficeRunService(store, &officeRecoveryBackend{state})
			_, err := first.Create(OfficeCallerPlugin, actor, request)
			require.ErrorIs(t, err, errOfficeRecoveryCrash)
			op, err := store.Get(OfficeCallerPlugin, request.OperationKey)
			require.NoError(t, err)
			var canonical OfficePreparedRun
			require.NoError(t, json.Unmarshal(op.PreparedJSON, &canonical))
			state.templateTitle = "Edited after interruption"
			restarted := NewOfficeRunService(store, &officeRecoveryBackend{state})
			result, err := restarted.Create(OfficeCallerPlugin, actor, request)
			require.NoError(t, err)
			require.Equal(t, op.RunID, result.RunID)
			require.Len(t, state.channels, 1)
			require.Len(t, state.runs, 1)
			require.Equal(t, 1, state.preparations)
			saved := state.runs[result.RunID]
			require.Equal(t, canonical.Run.Clone().Checklists, saved.Checklists)
			require.Equal(t, "Original checklist", saved.Checklists[0].Items[0].Title)
			require.Equal(t, request.OwnerUserID, saved.OwnerUserID)
			require.Equal(t, request.Summary, saved.Summary)
			lookup, err := restarted.ByKey(OfficeCallerPlugin, actor, request.OperationKey)
			require.NoError(t, err)
			require.Equal(t, result, lookup)
		})
	}
}

func TestOfficeRunConcurrentRequestsAndReplayAuthorization(t *testing.T) {
	store, state := newOfficeRecoveryFixture()
	actor := model.NewId()
	request := validOfficeRunRequest()
	const count = 16
	results := make([]*OfficeRunResult, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			svc := NewOfficeRunService(store, &officeRecoveryBackend{state})
			results[index], errs[index] = svc.Create(OfficeCallerPlugin, actor, request)
		}(i)
	}
	wg.Wait()
	for i := range count {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
	require.Len(t, state.channels, 1)
	require.Len(t, state.runs, 1)
	require.Equal(t, 1, state.preparations)
	require.Equal(t, 1, state.provisions)
	svc := NewOfficeRunService(store, &officeRecoveryBackend{state})
	changed := request
	changed.Summary = "Changed payload"
	_, err := svc.Create(OfficeCallerPlugin, actor, changed)
	require.ErrorIs(t, err, ErrOfficeConflict)
	_, err = svc.Create(OfficeCallerPlugin, model.NewId(), request)
	require.ErrorIs(t, err, ErrOfficeNotFound)
	_, err = svc.ByKey(OfficeCallerPlugin, model.NewId(), request.OperationKey)
	require.ErrorIs(t, err, ErrOfficeNotFound)
	state.denyRead = true
	_, err = svc.Create(OfficeCallerPlugin, actor, request)
	require.ErrorIs(t, err, ErrOfficeNotFound)
	_, err = svc.ByKey(OfficeCallerPlugin, actor, request.OperationKey)
	require.ErrorIs(t, err, ErrOfficeNotFound)
	require.Equal(t, 1, state.provisions)
}

func TestOfficeRunPendingRecoveryChecksCurrentCreationPermission(t *testing.T) {
	store, state := newOfficeRecoveryFixture()
	state.fault = "after channel"
	actor := model.NewId()
	request := validOfficeRunRequest()
	_, err := NewOfficeRunService(store, &officeRecoveryBackend{state}).Create(OfficeCallerPlugin, actor, request)
	require.ErrorIs(t, err, errOfficeRecoveryCrash)
	state.denyCreate = true
	_, err = NewOfficeRunService(store, &officeRecoveryBackend{state}).Create(OfficeCallerPlugin, actor, request)
	require.ErrorIs(t, err, ErrOfficeForbidden)
	require.Len(t, state.channels, 1)
	require.Empty(t, state.runs)
	require.Equal(t, 1, state.provisions)
}
