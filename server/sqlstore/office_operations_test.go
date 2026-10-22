// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

func TestOfficeOperationConcurrentClaimAndRestart(t *testing.T) {
	db := setupTestDB(t)
	store := setupSQLStore(t, db)
	require.NoError(t, createOfficeOperationTables(db), "migration is repeatable")
	s := NewOfficeOperationStore(store)
	request := app.OfficeRunRequest{OperationKey: "request:1:delivery", RequestID: "request-1", Purpose: "delivery", Generation: 1, SourceVersion: 1, PlaybookID: model.NewId(), OwnerUserID: model.NewId(), TeamID: model.NewId(), Name: "Delivery"}
	actor := model.NewId()
	const count = 12
	results := make([]*app.OfficeOperation, count)
	errors := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errors[i] = s.Claim(app.OfficeCallerPlugin, actor, request) }(i)
	}
	wg.Wait()
	for i := range count {
		require.NoError(t, errors[i])
		require.Equal(t, results[0].RunID, results[i].RunID)
	}
	prepared := &app.OfficePreparedRun{Run: &app.PlaybookRun{ID: results[0].RunID, Name: "Frozen"}, ChannelCreatorID: model.NewId()}
	saved, err := s.SavePrepared(app.OfficeCallerPlugin, request.OperationKey, prepared)
	require.NoError(t, err)
	require.Equal(t, "Frozen", saved.Run.Name)
	prepared.Run.Name = "Template changed"
	restarted := NewOfficeOperationStore(store)
	saved, err = restarted.SavePrepared(app.OfficeCallerPlugin, request.OperationKey, prepared)
	require.NoError(t, err)
	require.Equal(t, "Frozen", saved.Run.Name)
	op, err := restarted.Get(app.OfficeCallerPlugin, request.OperationKey)
	require.NoError(t, err)
	var decoded app.OfficePreparedRun
	require.NoError(t, json.Unmarshal(op.PreparedJSON, &decoded))
	require.Equal(t, saved.ChannelCreatorID, decoded.ChannelCreatorID)
	changed := request
	changed.Summary = "different payload"
	op, err = restarted.Claim(app.OfficeCallerPlugin, model.NewId(), changed)
	require.NoError(t, err)
	require.Equal(t, actor, op.ActorUserID)
	require.Equal(t, request.Fingerprint(), op.Fingerprint)
	require.ErrorIs(t, s.Complete(app.OfficeCallerPlugin, request.OperationKey), app.ErrOfficeConflict, "cannot complete without a persisted run")
}

func TestOfficeOperationSessionLockSerializesWithoutTransaction(t *testing.T) {
	db := setupTestDB(t)
	s := NewOfficeOperationStore(setupSQLStore(t, db))
	firstEntered := make(chan struct{})
	secondAttempted := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan error, 2)
	go func() {
		completed <- s.WithLock(app.OfficeCallerPlugin, "key", func() error { close(firstEntered); <-release; return nil })
	}()
	<-firstEntered
	go func() {
		close(secondAttempted)
		completed <- s.WithLock(app.OfficeCallerPlugin, "key", func() error { return nil })
	}()
	<-secondAttempted
	// Other DB statements remain possible while the provisioning worker holds
	// the session lock. No transaction is retained across the callback.
	var one int
	require.NoError(t, db.Get(&one, "SELECT 1"))
	require.Equal(t, 1, one)
	close(release)
	require.NoError(t, <-completed)
	require.NoError(t, <-completed)
	require.NoError(t, s.WithLock(app.OfficeCallerPlugin, "key", func() error { return nil }))
}
