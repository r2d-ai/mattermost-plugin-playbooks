// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app_test

import (
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-playbooks/server/app"
	mock_app "github.com/mattermost/mattermost-plugin-playbooks/server/app/mocks"
)

func TestInsightsWithoutHostLicense(t *testing.T) {
	for _, teamInsights := range []bool{false, true} {
		for _, guest := range []bool{false, true} {
			t.Run(fmtInsightCase(teamInsights, guest), func(t *testing.T) {
				ctrl := gomock.NewController(t)
				store := mock_app.NewMockPlaybookStore(ctrl)
				api := &plugintest.API{}
				user := &model.User{Id: model.NewId(), Roles: "system_user"}
				if guest {
					user.Roles = "system_guest"
				}
				api.On("GetUser", user.Id).Return(user, (*model.AppError)(nil)).Once()
				// Any GetLicense call is unexpected: this works on an unlicensed host.
				service := app.NewPlaybookService(store, nil, pluginapi.NewClient(api, nil), nil, nil, nil, nil)
				opts := &app.InsightsOpts{}
				expected := &app.PlaybooksInsightsList{}
				if !guest {
					if teamInsights {
						store.EXPECT().GetTopPlaybooksForTeam("team", user.Id, opts).Return(expected, nil)
					} else {
						store.EXPECT().GetTopPlaybooksForUser("team", user.Id, opts).Return(expected, nil)
					}
				}
				var result *app.PlaybooksInsightsList
				var err error
				if teamInsights {
					result, err = service.GetTopPlaybooksForTeam("team", user.Id, opts)
				} else {
					result, err = service.GetTopPlaybooksForUser("team", user.Id, opts)
				}
				if guest {
					require.ErrorContains(t, err, "Guests aren't authorized")
					require.Nil(t, result)
				} else {
					require.NoError(t, err)
					require.Same(t, expected, result)
				}
				api.AssertExpectations(t)
			})
		}
	}
}

func fmtInsightCase(team, guest bool) string {
	name := "personal"
	if team {
		name = "team"
	}
	if guest {
		return name + "/guest"
	}
	return name + "/member"
}
