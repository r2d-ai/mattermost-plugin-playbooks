// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	mock_app "github.com/mattermost/mattermost-plugin-playbooks/server/app/mocks"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
)

type officeAPIRunsStub struct {
	result             *app.OfficeRunResult
	err                error
	caller, actor, key string
	request            app.OfficeRunRequest
	calls              int
}

func (s *officeAPIRunsStub) Create(caller, actor string, request app.OfficeRunRequest) (*app.OfficeRunResult, error) {
	s.caller, s.actor, s.request = caller, actor, request
	s.calls++
	return s.result, s.err
}
func (s *officeAPIRunsStub) ByKey(caller, actor, key string) (*app.OfficeRunResult, error) {
	s.caller, s.actor, s.key = caller, actor, key
	s.calls++
	return s.result, s.err
}

type officeAPITaskSourceStub struct {
	run *app.PlaybookRun
}

func (s *officeAPITaskSourceStub) GetRunsWithAssignedTasks(string) ([]app.AssignedRun, error) {
	if s.run == nil {
		return nil, nil
	}
	return []app.AssignedRun{{RunLink: app.RunLink{PlaybookRunID: s.run.ID}}}, nil
}
func (s *officeAPITaskSourceStub) GetPlaybookRun(string) (*app.PlaybookRun, error) {
	if s.run == nil {
		return nil, app.ErrNotFound
	}
	return s.run, nil
}

type officeAPIPermissionsStub struct{ runErr, channelErr error }

func (s *officeAPIPermissionsStub) RunView(string, string) error     { return s.runErr }
func (s *officeAPIPermissionsStub) ChannelView(string, string) error { return s.channelErr }

type officeAPIGatesStub struct {
	gates     []app.OfficeGate
	projected bool
	listed    bool
	err       error
}

func (s *officeAPIGatesStub) Project(string, string, app.OfficeGateProjection) (*app.OfficeGateResult, error) {
	s.projected = true
	return &app.OfficeGateResult{}, s.err
}
func (s *officeAPIGatesStub) List(string) ([]app.OfficeGate, error) {
	s.listed = true
	return s.gates, s.err
}

type officeAPIFixture struct {
	handler     *Handler
	plugin      *plugintest.API
	runs        *officeAPIRunsStub
	source      *officeAPITaskSourceStub
	permissions *officeAPIPermissionsStub
	gates       *officeAPIGatesStub
}

func newOfficeAPIFixture(t *testing.T) *officeAPIFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	auditor := mock_app.NewMockAuditor(ctrl)
	auditor.EXPECT().MakeAuditRecord("interPluginActAsUser", model.AuditStatusSuccess).Return(&model.AuditRecord{}).AnyTimes()
	auditor.EXPECT().LogAuditRec(gomock.Any()).AnyTimes()
	plugin := &plugintest.API{}
	client := pluginapi.NewClient(plugin, &plugintest.Driver{})
	f := &officeAPIFixture{handler: NewHandler(client, nil, auditor), plugin: plugin, runs: &officeAPIRunsStub{}, source: &officeAPITaskSourceStub{}, permissions: &officeAPIPermissionsStub{}, gates: &officeAPIGatesStub{}}
	NewOfficeHandler(f.handler, client, f.permissions, f.source, f.runs, f.gates)
	return f
}
func (f *officeAPIFixture) serve(method, path, body, caller, actor, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if caller != "" {
		r.Header.Set(pluginIDHeader, caller)
	}
	if actor != "" {
		r.Header.Set(actingUserIDHeader, actor)
	}
	if user != "" {
		r.Header.Set(userIDHeader, user)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestOfficeAuthorization(t *testing.T) {
	actor := model.NewId()
	for _, test := range []struct {
		name, caller, actor, user string
		active                    bool
		status                    int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "browser cannot use Office", user: actor, status: http.StatusForbidden},
		{name: "other plugin", caller: "other", actor: actor, status: http.StatusForbidden},
		{name: "missing actor", caller: app.OfficeCallerPlugin, user: actor, status: http.StatusUnauthorized},
		{name: "invalid actor", caller: app.OfficeCallerPlugin, actor: "bad", status: http.StatusUnauthorized},
		{name: "conflicting actor", caller: app.OfficeCallerPlugin, actor: actor, user: model.NewId(), status: http.StatusForbidden},
		{name: "inactive actor", caller: app.OfficeCallerPlugin, actor: actor, status: http.StatusForbidden},
		{name: "trusted actor", caller: app.OfficeCallerPlugin, actor: actor, active: true, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOfficeAPIFixture(t)
			if test.name == "inactive actor" || test.active {
				user := &model.User{Id: actor}
				if !test.active {
					user.DeleteAt = 1
				}
				f.plugin.On("GetUser", actor).Return(user, (*model.AppError)(nil)).Once()
			}
			w := f.serve(http.MethodGet, "/api/v0/office/capabilities", "", test.caller, test.actor, test.user)
			require.Equal(t, test.status, w.Code, w.Body.String())
			if test.active {
				var response struct {
					SchemaVersion int      `json:"schema_version"`
					Capabilities  []string `json:"capabilities"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
				require.Equal(t, 1, response.SchemaVersion)
				require.Equal(t, []string{"scoped-task-inbox", "idempotent-runs", "external-approval-gates"}, response.Capabilities)
			}
			f.plugin.AssertExpectations(t)
		})
	}
}

func TestOfficeTasksQueryValidation(t *testing.T) {
	actor := model.NewId()
	for _, query := range []string{"limit=0", "limit=101", "limit=no", "limit=1&limit=2", "user_id=" + actor, "unknown=value", "cursor=bad!", "cursor=" + strings.Repeat("a", 2049), "limit=", "limit=%ZZ"} {
		t.Run(query[:min(len(query), 30)], func(t *testing.T) {
			f := newOfficeAPIFixture(t)
			f.plugin.On("GetUser", actor).Return(&model.User{Id: actor}, (*model.AppError)(nil))
			w := f.serve(http.MethodGet, "/api/v0/office/tasks?"+query, "", app.OfficeCallerPlugin, actor, "")
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
	f := newOfficeAPIFixture(t)
	f.plugin.On("GetUser", actor).Return(&model.User{Id: actor}, (*model.AppError)(nil))
	w := f.serve(http.MethodGet, "/api/v0/office/tasks", "", app.OfficeCallerPlugin, actor, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"items":[],"next_cursor":""}`, w.Body.String())
}

func TestOfficeRunRoutesAndErrors(t *testing.T) {
	actor := model.NewId()
	f := newOfficeAPIFixture(t)
	f.plugin.On("GetUser", actor).Return(&model.User{Id: actor}, (*model.AppError)(nil))
	f.runs.result = &app.OfficeRunResult{RunID: model.NewId(), ChannelID: model.NewId(), Path: "/playbooks/runs/run"}
	w := f.serve(http.MethodPost, "/api/v0/office/runs", `{"operation_key":"office:request:run","request_id":"request","generation":1}`, app.OfficeCallerPlugin, actor, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, app.OfficeCallerPlugin, f.runs.caller)
	require.Equal(t, actor, f.runs.actor)
	require.Equal(t, "office:request:run", f.runs.request.OperationKey)
	w = f.serve(http.MethodGet, "/api/v0/office/runs/by-key?operation_key=office%3Arequest%3Arun", "", app.OfficeCallerPlugin, actor, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "office:request:run", f.runs.key)
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `{`, strings.Repeat(" ", 65537) + `{}`} {
		calls := f.runs.calls
		w = f.serve(http.MethodPost, "/api/v0/office/runs", body, app.OfficeCallerPlugin, actor, "")
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.Equal(t, calls, f.runs.calls)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{app.ErrOfficeInvalid, 400}, {app.ErrOfficeForbidden, 403}, {app.ErrOfficeNotFound, 404}, {app.ErrOfficeConflict, 409}, {app.ErrOfficeBlocked, 409}} {
		f.runs.err = test.err
		w = f.serve(http.MethodGet, "/api/v0/office/runs/by-key?operation_key=key", "", app.OfficeCallerPlugin, actor, "")
		require.Equal(t, test.status, w.Code, w.Body.String())
	}
}

func TestOfficeApprovalGatesRequireRunAndChannelAccess(t *testing.T) {
	actor, runID := model.NewId(), model.NewId()
	for _, test := range []struct {
		name               string
		runErr, channelErr error
		status             int
	}{
		{name: "readable", status: http.StatusOK},
		{name: "run denied", runErr: app.ErrNoPermissions, status: http.StatusForbidden},
		{name: "channel denied", channelErr: app.ErrNoPermissions, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOfficeAPIFixture(t)
			f.plugin.On("GetUser", actor).Return(&model.User{Id: actor}, (*model.AppError)(nil))
			f.source.run = &app.PlaybookRun{ID: runID, ChannelID: model.NewId()}
			f.permissions.runErr, f.permissions.channelErr = test.runErr, test.channelErr
			w := f.serve(http.MethodGet, "/api/v0/runs/"+runID+"/approval-gates", "", "", "", actor)
			require.Equal(t, test.status, w.Code, w.Body.String())
			require.Equal(t, test.status == http.StatusOK, f.gates.listed)
			if test.status == http.StatusOK {
				require.JSONEq(t, `[]`, w.Body.String())
			}
			if test.status != http.StatusOK {
				w = f.serve(http.MethodPost, "/api/v0/office/runs/"+runID+"/approval-gates", `{}`, app.OfficeCallerPlugin, actor, "")
				require.Equal(t, test.status, w.Code, w.Body.String())
				require.False(t, f.gates.projected)
			}
		})
	}
}

func TestOfficeApprovalGateViewsAndProjection(t *testing.T) {
	actor, runID := model.NewId(), model.NewId()
	f := newOfficeAPIFixture(t)
	f.plugin.On("GetUser", actor).Return(&model.User{Id: actor}, (*model.AppError)(nil))
	f.source.run = &app.PlaybookRun{ID: runID, ChannelID: model.NewId()}
	projection := app.OfficeGateProjection{SourceKey: "office:request:approval", SourceVersion: 1, RequestID: "request", ApprovalID: "approval", Title: "Approve purchase", Status: "pending", AssigneeUserID: actor, RequesterUserID: model.NewId(), Guards: []string{"NotRequester"}}
	f.gates.gates = []app.OfficeGate{{ID: model.NewId(), RunID: runID, CallerPlugin: app.OfficeCallerPlugin, Projection: projection}}
	w := f.serve(http.MethodGet, "/api/v0/runs/"+runID+"/approval-gates", "", "", "", actor)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var views []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &views))
	require.Len(t, views, 1)
	require.Len(t, views[0], 5)
	require.Equal(t, true, views[0]["can_review"])
	require.Equal(t, "/office?page=%2Fwork%2Fapprovals%2Fapproval", views[0]["path"])
	require.NotContains(t, w.Body.String(), projection.RequesterUserID)
	require.NotContains(t, w.Body.String(), "source_key")
	require.NotContains(t, w.Body.String(), "guards")
	data, err := json.Marshal(projection)
	require.NoError(t, err)
	w = f.serve(http.MethodPost, "/api/v0/office/runs/"+runID+"/approval-gates", string(data), app.OfficeCallerPlugin, actor, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, f.gates.projected)
	f.gates.projected = false
	w = f.serve(http.MethodPost, "/api/v0/office/runs/"+runID+"/approval-gates", `{}`, app.OfficeCallerPlugin, actor, "")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.False(t, f.gates.projected)
}
