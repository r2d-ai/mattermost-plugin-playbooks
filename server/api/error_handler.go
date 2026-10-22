// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package api

import (
	"errors"
	"net/http"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	"github.com/sirupsen/logrus"
)

type ErrorHandler struct {
}

// HandleError reports expected approval conflicts without treating them as outages.
func (h *ErrorHandler) HandleError(w http.ResponseWriter, logger logrus.FieldLogger, internalErr error) {
	if errors.Is(internalErr, app.ErrOfficeBlocked) {
		h.HandleErrorWithCode(w, logger, http.StatusConflict, "Office approval is required before changing this task or finishing the run.", internalErr)
		return
	}
	h.HandleErrorWithCode(w, logger, http.StatusInternalServerError, "An internal error has occurred. Check app server logs for details.", internalErr)
}

// HandleErrorWithCode logs the internal error and sends the public facing error
// message as JSON in a response with the provided code.
func (h *ErrorHandler) HandleErrorWithCode(w http.ResponseWriter, logger logrus.FieldLogger, code int, publicErrorMsg string, internalErr error) {
	HandleErrorWithCode(logger, w, code, publicErrorMsg, internalErr)
}

// PermissionsCheck handles the output of a permission check
// Automatically does the proper error handling.
// Returns true if the check passed and false on failure. Correct use is: if !h.PermissionsCheck(w, check) { return }
func (h *ErrorHandler) PermissionsCheck(w http.ResponseWriter, logger logrus.FieldLogger, checkOutput error) bool {
	if checkOutput != nil {
		h.HandleErrorWithCode(w, logger, http.StatusForbidden, "Not authorized", checkOutput)
		return false
	}

	return true
}
