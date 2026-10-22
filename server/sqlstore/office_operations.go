// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/mattermost/mattermost/server/public/model"
)

type OfficeOperationStore struct{ store *SQLStore }

func NewOfficeOperationStore(store *SQLStore) *OfficeOperationStore {
	return &OfficeOperationStore{store: store}
}

func createOfficeOperationTables(e sqlx.Ext) error {
	_, err := e.Exec(`CREATE TABLE IF NOT EXISTS IR_OfficeOperation (
		CallerPlugin VARCHAR(64) NOT NULL,
		OperationKey VARCHAR(256) NOT NULL,
		ActorUserID VARCHAR(26) NOT NULL,
		Fingerprint VARCHAR(64) NOT NULL,
		RequestID VARCHAR(128) NOT NULL,
		RunID VARCHAR(26) NOT NULL UNIQUE,
		PayloadJSON JSONB NOT NULL,
		PreparedJSON JSONB,
		Completed BOOLEAN NOT NULL DEFAULT FALSE,
		CreateAt BIGINT NOT NULL,
		UpdateAt BIGINT NOT NULL,
		PRIMARY KEY (CallerPlugin, OperationKey)
	)`)
	return err
}

// WithLock holds a session advisory lock, not a transaction, across host API calls.
// The reserved run ID and channel correlation remain authoritative even if the
// worker loses this connection; a replacement cannot allocate another resource.
func (s *OfficeOperationStore) WithLock(caller, key string, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := s.store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	hash := sha256.Sum256([]byte(caller + "\x00" + key))
	lockID := int64(binary.BigEndian.Uint64(hash[:8]))
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return err
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		if _, unlockErr := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID); unlockErr != nil {
			// A session with a possibly-held lock must never return to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn()
}

func (s *OfficeOperationStore) Claim(caller, actor string, request app.OfficeRunRequest) (*app.OfficeOperation, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	now := model.GetMillis()
	_, err = s.store.db.Exec(`INSERT INTO IR_OfficeOperation
		(CallerPlugin,OperationKey,ActorUserID,Fingerprint,RequestID,RunID,PayloadJSON,CreateAt,UpdateAt)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8) ON CONFLICT (CallerPlugin,OperationKey) DO NOTHING`,
		caller, request.OperationKey, actor, request.Fingerprint(), request.RequestID, model.NewId(), string(data), now)
	if err != nil {
		return nil, err
	}
	return s.Get(caller, request.OperationKey)
}

func (s *OfficeOperationStore) Get(caller, key string) (*app.OfficeOperation, error) {
	var op app.OfficeOperation
	var payload, prepared []byte
	err := s.store.db.QueryRow(`SELECT CallerPlugin,OperationKey,ActorUserID,Fingerprint,RequestID,RunID,PayloadJSON,PreparedJSON,Completed FROM IR_OfficeOperation WHERE CallerPlugin=$1 AND OperationKey=$2`, caller, key).
		Scan(&op.CallerPlugin, &op.OperationKey, &op.ActorUserID, &op.Fingerprint, &op.RequestID, &op.RunID, &payload, &prepared, &op.Completed)
	op.PayloadJSON, op.PreparedJSON = payload, prepared
	if errors.Is(err, sql.ErrNoRows) {
		return nil, app.ErrOfficeNotFound
	}
	return &op, err
}

func (s *OfficeOperationStore) SavePrepared(caller, key string, prepared *app.OfficePreparedRun) (*app.OfficePreparedRun, error) {
	data, err := json.Marshal(prepared)
	if err != nil {
		return nil, err
	}
	if len(data) > maxJSONLength {
		return nil, app.ErrOfficeInvalid
	}
	_, err = s.store.db.Exec(`UPDATE IR_OfficeOperation SET PreparedJSON=$3,UpdateAt=$4 WHERE CallerPlugin=$1 AND OperationKey=$2 AND PreparedJSON IS NULL AND RunID=$5`, caller, key, string(data), model.GetMillis(), prepared.Run.ID)
	if err != nil {
		return nil, err
	}
	op, err := s.Get(caller, key)
	if err != nil {
		return nil, err
	}
	var saved app.OfficePreparedRun
	if err = json.Unmarshal(op.PreparedJSON, &saved); err != nil {
		return nil, err
	}
	return &saved, nil
}

func (s *OfficeOperationStore) Complete(caller, key string) error {
	result, err := s.store.db.Exec(`UPDATE IR_OfficeOperation SET Completed=TRUE,UpdateAt=$3
		WHERE CallerPlugin=$1 AND OperationKey=$2 AND PreparedJSON IS NOT NULL
		AND EXISTS (SELECT 1 FROM IR_Incident WHERE ID=IR_OfficeOperation.RunID AND DeleteAt=0)`, caller, key, model.GetMillis())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return app.ErrOfficeConflict
	}
	return err
}
