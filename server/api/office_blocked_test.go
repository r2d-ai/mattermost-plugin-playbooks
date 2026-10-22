// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryMutationReportsOfficeApprovalRequired(t *testing.T) {
	w := httptest.NewRecorder()
	(&ErrorHandler{}).HandleError(w, logrus.New(), fmt.Errorf("persist task: %w", app.ErrOfficeBlocked))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "Office approval")
	require.NotContains(t, w.Body.String(), "persist task")
}
