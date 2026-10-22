// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// This fixture belongs only in the disposable Office smoke-test host. It lets
// that host's administrator exercise genuine PluginHTTP caller authentication.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

type callerPlugin struct {
	plugin.MattermostPlugin
}

type relayRequest struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	ActorUserID string          `json:"actor_user_id"`
	UserID      string          `json:"user_id,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
}

func (p *callerPlugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if !model.IsValidId(userID) || !p.API.HasPermissionTo(userID, model.PermissionManageSystem) {
		http.Error(w, "Smoke fixture requires a system administrator", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/relay" {
		http.NotFound(w, r)
		return
	}

	var input relayRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "Invalid relay request", http.StatusBadRequest)
		return
	}
	path, err := url.ParseRequestURI(input.Path)
	if err != nil || path.IsAbs() || path.Fragment != "" ||
		!strings.HasPrefix(path.Path, "/api/v0/office/") || strings.Contains(path.Path, "..") ||
		(input.Method != http.MethodGet && input.Method != http.MethodPost) {
		http.Error(w, "Only Office smoke requests are supported", http.StatusBadRequest)
		return
	}
	request, err := http.NewRequest(input.Method, "/playbooks"+input.Path, bytes.NewReader(input.Body))
	if err != nil {
		http.Error(w, "Invalid target", http.StatusBadRequest)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if input.ActorUserID != "" {
		request.Header.Set("Mattermost-Plugin-Acting-User-Id", input.ActorUserID)
	}
	if input.UserID != "" {
		request.Header.Set("Mattermost-User-ID", input.UserID)
	}
	response := p.API.PluginHTTP(request)
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func main() {
	plugin.ClientMain(&callerPlugin{})
}
