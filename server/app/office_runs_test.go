// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

func validOfficeRunRequest() OfficeRunRequest {
	return OfficeRunRequest{OperationKey: "procurement:42:1", RequestID: "request-42", Purpose: "delivery", Generation: 1, SourceVersion: 1, PlaybookID: model.NewId(), OwnerUserID: model.NewId(), TeamID: model.NewId(), Name: "Delivery"}
}

func TestOfficeRunRequestValidation(t *testing.T) {
	r := validOfficeRunRequest()
	require.NoError(t, r.Validate())
	for _, mutate := range []func(*OfficeRunRequest){
		func(r *OfficeRunRequest) { r.OperationKey = "" },
		func(r *OfficeRunRequest) { r.RequestID = "../private" },
		func(r *OfficeRunRequest) { r.RequestID = "request:2027.annual" },
		func(r *OfficeRunRequest) { r.Generation = 0 },
		func(r *OfficeRunRequest) { r.SourceVersion = -1 },
		func(r *OfficeRunRequest) { r.PlaybookID = "bad" },
		func(r *OfficeRunRequest) { r.OwnerUserID = "" },
		func(r *OfficeRunRequest) { r.TeamID = "" },
	} {
		copy := r
		mutate(&copy)
		require.ErrorIs(t, copy.Validate(), ErrOfficeInvalid)
	}
}

func TestOfficeRunFingerprintIncludesEntireSemanticPayload(t *testing.T) {
	r := validOfficeRunRequest()
	original := r.Fingerprint()
	require.Equal(t, original, r.Fingerprint())
	for _, mutate := range []func(*OfficeRunRequest){
		func(r *OfficeRunRequest) { r.RequestID += "2" },
		func(r *OfficeRunRequest) { r.Purpose += "2" },
		func(r *OfficeRunRequest) { r.Generation++ },
		func(r *OfficeRunRequest) { r.SourceVersion++ },
		func(r *OfficeRunRequest) { r.Summary += "changed" },
		func(r *OfficeRunRequest) { r.OwnerUserID = model.NewId() },
	} {
		copy := r
		mutate(&copy)
		require.NotEqual(t, original, copy.Fingerprint())
	}
}
