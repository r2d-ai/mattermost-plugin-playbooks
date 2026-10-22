// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.enterprise for license information.

package enterprise

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilitiesWithoutHostLicense(t *testing.T) {
	// A nil client proves capability checks do not consult license or dev settings.
	checker := NewLicenseChecker(nil)
	require.True(t, checker.PlaybookAllowed(false))
	require.True(t, checker.PlaybookAllowed(true))
	require.True(t, checker.RetrospectiveAllowed())
	require.True(t, checker.TimelineAllowed())
	require.True(t, checker.StatsAllowed())
	require.True(t, checker.ChecklistItemDueDateAllowed())
	require.True(t, checker.PlaybookAttributesAllowed())
	require.True(t, checker.ConditionalPlaybooksAllowed())
}
