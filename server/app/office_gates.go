// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
)

// OfficeGateProjection is written only by the authenticated Office plugin.
// Actor identities and guards are private projection data, never run metadata.
type OfficeGateProjection struct {
	SourceKey            string   `json:"source_key"`
	SourceVersion        int64    `json:"source_version"`
	RequestID            string   `json:"request_id"`
	ApprovalID           string   `json:"approval_id"`
	Title                string   `json:"title"`
	RequesterUserID      string   `json:"requester_user_id"`
	CreatorUserID        string   `json:"creator_user_id"`
	SubmitterUserID      string   `json:"submitter_user_id"`
	PriceConfirmerUserID string   `json:"price_confirmer_user_id"`
	AssigneeUserID       string   `json:"assignee_user_id"`
	Guards               []string `json:"guards"`
	Status               string   `json:"status"`
	DependentTaskIDs     []string `json:"dependent_task_ids,omitempty"`
}

// OfficeGate deliberately has no JSON representation. Call View for presentation.
type OfficeGate struct {
	ID           string               `json:"-"`
	RunID        string               `json:"-"`
	CallerPlugin string               `json:"-"`
	Projection   OfficeGateProjection `json:"-"`
}

type OfficeGateResult struct {
	GateID        string `json:"gate_id"`
	SourceKey     string `json:"source_key"`
	SourceVersion int64  `json:"source_version"`
	Status        string `json:"status"`
}

type OfficeGateView struct {
	GateID    string `json:"gate_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Path      string `json:"path"`
	CanReview bool   `json:"can_review"`
}

type OfficeGateStore interface {
	Project(caller, runID string, projection OfficeGateProjection) (*OfficeGateResult, error)
	List(runID string) ([]OfficeGate, error)
	BlocksTask(runID, taskID string) error
}

func (p OfficeGateProjection) Validate() error {
	if !validOfficeGateText(p.SourceKey, 256) || p.SourceVersion <= 0 ||
		!validOfficeGateToken(p.RequestID) || !validOfficeGateToken(p.ApprovalID) || !validOfficeGateText(p.Title, 256) {
		return fmt.Errorf("%w: invalid approval identity, title, or version", ErrOfficeInvalid)
	}
	for _, id := range []string{p.RequesterUserID, p.CreatorUserID, p.SubmitterUserID, p.PriceConfirmerUserID, p.AssigneeUserID} {
		if id != "" && !model.IsValidId(id) {
			return fmt.Errorf("%w: invalid approval actor", ErrOfficeInvalid)
		}
	}
	switch p.Status {
	case "pending", "approved", "rejected", "returned", "cancelled":
	default:
		return fmt.Errorf("%w: invalid approval status", ErrOfficeInvalid)
	}
	seen := make(map[string]bool, len(p.Guards))
	for _, guard := range p.Guards {
		if seen[guard] {
			return fmt.Errorf("%w: duplicate approval guard", ErrOfficeInvalid)
		}
		seen[guard] = true
		switch guard {
		case "NotRequester":
			if p.RequesterUserID == "" {
				return fmt.Errorf("%w: requester required by guard", ErrOfficeInvalid)
			}
		case "ApproverNotCreatorOrSubmitter":
			if p.CreatorUserID == "" || p.SubmitterUserID == "" {
				return fmt.Errorf("%w: creator and submitter required by guard", ErrOfficeInvalid)
			}
		case "NotPriceConfirmer":
			if p.PriceConfirmerUserID == "" {
				return fmt.Errorf("%w: price confirmer required by guard", ErrOfficeInvalid)
			}
		default:
			return fmt.Errorf("%w: unknown approval guard", ErrOfficeInvalid)
		}
	}
	if len(p.DependentTaskIDs) > 1000 {
		return fmt.Errorf("%w: too many dependent tasks", ErrOfficeInvalid)
	}
	seen = make(map[string]bool, len(p.DependentTaskIDs))
	for _, id := range p.DependentTaskIDs {
		if !model.IsValidId(id) || seen[id] {
			return fmt.Errorf("%w: invalid or duplicate dependent task", ErrOfficeInvalid)
		}
		seen[id] = true
	}
	return nil
}

func validOfficeGateText(value string, limit int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(value) <= limit &&
		strings.IndexFunc(value, unicode.IsControl) == -1
}

func validOfficeGateToken(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// CanReview checks assignment and separation of duties. The caller checks that
// the user is active and may read this run and its channel before exposing View.
func (g OfficeGate) CanReview(actorID string) bool {
	p := g.Projection
	if p.Status != "pending" || actorID == "" || actorID != p.AssigneeUserID || p.Validate() != nil {
		return false
	}
	for _, guard := range p.Guards {
		switch guard {
		case "NotRequester":
			if actorID == p.RequesterUserID {
				return false
			}
		case "ApproverNotCreatorOrSubmitter":
			if actorID == p.CreatorUserID || actorID == p.SubmitterUserID {
				return false
			}
		case "NotPriceConfirmer":
			if actorID == p.PriceConfirmerUserID {
				return false
			}
		}
	}
	return true
}

func (g OfficeGate) View(actorID string) OfficeGateView {
	return OfficeGateView{
		GateID: g.ID, Title: g.Projection.Title, Status: g.Projection.Status,
		Path:      "/office?page=" + url.QueryEscape("/work/approvals/"+g.Projection.ApprovalID),
		CanReview: g.CanReview(actorID),
	}
}
