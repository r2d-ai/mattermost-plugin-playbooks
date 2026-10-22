// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.enterprise for license information.

package enterprise

import (
	"github.com/mattermost/mattermost/server/public/pluginapi"
)

// LicenseChecker preserves the upstream capability interface. This fork enables
// all plugin-owned features independently of the host server's subscription.
// Authorization remains enforced by the API and PermissionsService.
type LicenseChecker struct{}

func NewLicenseChecker(_ *pluginapi.Client) *LicenseChecker {
	return &LicenseChecker{}
}

// PlaybookAllowed returns true for both public and private playbooks.
func (e *LicenseChecker) PlaybookAllowed(_ bool) bool {
	return true
}

// RetrospectiveAllowed returns true because retrospective is enabled in this fork.
func (e *LicenseChecker) RetrospectiveAllowed() bool {
	return true
}

// TimelineAllowed returns true because timeline is enabled in this fork.
func (e *LicenseChecker) TimelineAllowed() bool {
	return true
}

// StatsAllowed returns true because stats is enabled in this fork.
func (e *LicenseChecker) StatsAllowed() bool {
	return true
}

// ChecklistItemDueDateAllowed returns true if setting/editing checklist item due date is allowed.
func (e *LicenseChecker) ChecklistItemDueDateAllowed() bool {
	return true
}

// PlaybookAttributesAllowed returns true because playbook attributes is enabled in this fork.
func (e *LicenseChecker) PlaybookAttributesAllowed() bool {
	return true
}

// ConditionalPlaybooksAllowed returns true because conditional playbooks is enabled in this fork.
func (e *LicenseChecker) ConditionalPlaybooksAllowed() bool {
	return true
}
