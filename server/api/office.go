// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
)

const officeRequestMaxSize = 64 * 1024

type OfficeRuns interface {
	Create(caller, actor string, request app.OfficeRunRequest) (*app.OfficeRunResult, error)
	ByKey(caller, actor, key string) (*app.OfficeRunResult, error)
}

type OfficeGates interface {
	Project(caller, runID string, projection app.OfficeGateProjection) (*app.OfficeGateResult, error)
	List(runID string) ([]app.OfficeGate, error)
}

type OfficeHandler struct {
	pluginAPI   *pluginapi.Client
	permissions app.OfficeReadPermissions
	runService  app.OfficeTaskRunService
	runs        OfficeRuns
	gates       OfficeGates
	tasks       *app.OfficeTaskService
}

// NewOfficeHandler must be registered before the general /runs subrouter.
func NewOfficeHandler(handler *Handler, pluginAPI *pluginapi.Client, permissions app.OfficeReadPermissions, runService app.OfficeTaskRunService, runs OfficeRuns, gates OfficeGates) *OfficeHandler {
	h := &OfficeHandler{pluginAPI: pluginAPI, permissions: permissions, runService: runService, runs: runs, gates: gates, tasks: app.NewOfficeTaskService(runService, permissions)}
	office := handler.APIRouter.PathPrefix("/office").Subrouter()
	office.Use(h.requireOffice)
	office.HandleFunc("/capabilities", h.capabilities).Methods(http.MethodGet)
	office.HandleFunc("/tasks", h.listTasks).Methods(http.MethodGet)
	office.HandleFunc("/runs", h.createRun).Methods(http.MethodPost)
	office.HandleFunc("/runs/by-key", h.runByKey).Methods(http.MethodGet)
	office.HandleFunc("/runs/{run_id}/approval-gates", h.projectGates).Methods(http.MethodPost)
	handler.APIRouter.HandleFunc("/runs/{run_id}/approval-gates", h.listGates).Methods(http.MethodGet)
	return h
}

func (h *OfficeHandler) requireOffice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(pluginIDHeader) != app.OfficeCallerPlugin || len(r.Header.Values(pluginIDHeader)) != 1 {
			handleResponseWithCode(w, http.StatusForbidden, "Office caller required")
			return
		}
		actor := r.Header.Get(actingUserIDHeader)
		if !model.IsValidId(actor) || len(r.Header.Values(actingUserIDHeader)) != 1 {
			handleResponseWithCode(w, http.StatusUnauthorized, "Valid acting user required")
			return
		}
		if user := r.Header.Get(userIDHeader); (user != "" && user != actor) || len(r.Header.Values(userIDHeader)) > 1 {
			handleResponseWithCode(w, http.StatusForbidden, "Conflicting acting user")
			return
		}
		if !h.activeUser(actor) {
			handleResponseWithCode(w, http.StatusForbidden, "Active acting user required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *OfficeHandler) activeUser(actor string) bool {
	if !model.IsValidId(actor) {
		return false
	}
	user, err := h.pluginAPI.User.Get(actor)
	return err == nil && user != nil && user.Id == actor && user.DeleteAt == 0
}

func (h *OfficeHandler) capabilities(w http.ResponseWriter, r *http.Request) {
	if _, err := officeQuery(r); err != nil {
		h.fail(w, r, err)
		return
	}
	ReturnJSON(w, struct {
		SchemaVersion int      `json:"schema_version"`
		Capabilities  []string `json:"capabilities"`
	}{1, []string{"scoped-task-inbox", "idempotent-runs", "external-approval-gates"}}, http.StatusOK)
}

// officeQuery rejects overrides, duplicate values, malformed escaping and unknown
// filters rather than silently broadening a request's scope.
func officeQuery(r *http.Request, allowed ...string) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, app.ErrOfficeInvalid
	}
	for key, values := range query {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known || len(values) != 1 {
			return nil, app.ErrOfficeInvalid
		}
	}
	return query, nil
}

func (h *OfficeHandler) listTasks(w http.ResponseWriter, r *http.Request) {
	query, err := officeQuery(r, "limit", "cursor")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit := 20
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			h.fail(w, r, app.ErrOfficeInvalid)
			return
		}
	}
	page, err := h.tasks.List(r.Header.Get(actingUserIDHeader), limit, query.Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ReturnJSON(w, page, http.StatusOK)
}

func decodeOfficeBody(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, officeRequestMaxSize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return app.ErrOfficeInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return app.ErrOfficeInvalid
	}
	return nil
}

func (h *OfficeHandler) createRun(w http.ResponseWriter, r *http.Request) {
	if _, err := officeQuery(r); err != nil {
		h.fail(w, r, err)
		return
	}
	var request app.OfficeRunRequest
	if err := decodeOfficeBody(w, r, &request); err != nil {
		h.fail(w, r, err)
		return
	}
	result, err := h.runs.Create(r.Header.Get(pluginIDHeader), r.Header.Get(actingUserIDHeader), request)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ReturnJSON(w, result, http.StatusOK)
}

func (h *OfficeHandler) runByKey(w http.ResponseWriter, r *http.Request) {
	query, err := officeQuery(r, "operation_key")
	if err != nil || query.Get("operation_key") == "" {
		h.fail(w, r, app.ErrOfficeInvalid)
		return
	}
	result, err := h.runs.ByKey(r.Header.Get(pluginIDHeader), r.Header.Get(actingUserIDHeader), query.Get("operation_key"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ReturnJSON(w, result, http.StatusOK)
}

func (h *OfficeHandler) readableRun(actor, runID string) error {
	if !model.IsValidId(runID) {
		return app.ErrOfficeInvalid
	}
	if err := h.permissions.RunView(actor, runID); err != nil {
		return err
	}
	run, err := h.runService.GetPlaybookRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return app.ErrOfficeNotFound
	}
	if run.ChannelID == "" {
		return app.ErrOfficeForbidden
	}
	return h.permissions.ChannelView(actor, run.ChannelID)
}

func (h *OfficeHandler) projectGates(w http.ResponseWriter, r *http.Request) {
	if _, err := officeQuery(r); err != nil {
		h.fail(w, r, err)
		return
	}
	runID := mux.Vars(r)["run_id"]
	if err := h.readableRun(r.Header.Get(actingUserIDHeader), runID); err != nil {
		h.fail(w, r, err)
		return
	}
	var projection app.OfficeGateProjection
	if err := decodeOfficeBody(w, r, &projection); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := projection.Validate(); err != nil {
		h.fail(w, r, err)
		return
	}
	result, err := h.gates.Project(r.Header.Get(pluginIDHeader), runID, projection)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if lifecycle, ok := h.runService.(interface{ ReconcileOfficeGate(string) error }); ok {
		if err = lifecycle.ReconcileOfficeGate(runID); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	ReturnJSON(w, result, http.StatusOK)
}

func (h *OfficeHandler) listGates(w http.ResponseWriter, r *http.Request) {
	actor := r.Header.Get(userIDHeader)
	if !h.activeUser(actor) {
		h.fail(w, r, app.ErrOfficeForbidden)
		return
	}
	if _, err := officeQuery(r); err != nil {
		h.fail(w, r, err)
		return
	}
	runID := mux.Vars(r)["run_id"]
	if err := h.readableRun(actor, runID); err != nil {
		h.fail(w, r, err)
		return
	}
	gates, err := h.gates.List(runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	views := make([]app.OfficeGateView, 0, len(gates))
	for _, gate := range gates {
		views = append(views, gate.View(actor))
	}
	ReturnJSON(w, views, http.StatusOK)
}

func (h *OfficeHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusInternalServerError, "Office request failed"
	switch {
	case errors.Is(err, app.ErrOfficeInvalid):
		status, message = http.StatusBadRequest, "Invalid Office request"
	case errors.Is(err, app.ErrOfficeForbidden), errors.Is(err, app.ErrNoPermissions):
		status, message = http.StatusForbidden, "Office access denied"
	case errors.Is(err, app.ErrOfficeNotFound), errors.Is(err, app.ErrNotFound):
		status, message = http.StatusNotFound, "Office resource not found"
	case errors.Is(err, app.ErrOfficeConflict):
		status, message = http.StatusConflict, "Office operation conflicts with its saved version"
	case errors.Is(err, app.ErrOfficeBlocked):
		status, message = http.StatusConflict, "Office approval is required"
	}
	HandleErrorWithCode(getLogger(r), w, status, message, err)
}
