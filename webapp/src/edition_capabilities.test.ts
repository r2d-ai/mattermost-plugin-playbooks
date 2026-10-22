// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {GlobalState} from '@mattermost/types/store';

import {isConfiguredForDevelopment, isEnterpriseLicensedOrDevelopment, isProfessionalLicensedOrDevelopment} from 'src/license';

describe('fork capabilities', () => {
    it.each([
        {},
        {IsLicensed: 'false'},
        {IsLicensed: 'true', SkuShortName: 'professional'},
        {IsLicensed: 'true', SkuShortName: 'enterprise'},
        {IsLicensed: 'true', SkuShortName: 'entry'},
        {IsLicensed: 'true', SkuShortName: 'advanced'},
    ])('enables features without enabling development mode: %p', (license) => {
        const state = {
            entities: {
                general: {
                    config: {EnableDeveloper: 'false', EnableTesting: 'false'},
                    license,
                },
            },
        } as unknown as GlobalState;

        expect(isProfessionalLicensedOrDevelopment(state)).toBe(true);
        expect(isEnterpriseLicensedOrDevelopment(state)).toBe(true);
        expect(isConfiguredForDevelopment(state)).toBe(false);
    });
});
