// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fetchRunApprovalGates, setSiteUrl} from 'src/client';
import {ApprovalGate} from 'src/types/approval_gates';

jest.mock('src/actions', () => ({setTriggerId: jest.fn()}));

const gate: ApprovalGate = {
    gate_id: 'gate-1',
    title: 'Budget approval',
    status: 'pending',
    path: '/office?page=%2Fwork%2Fapprovals%2Frequest-1',
    can_review: true,
};

const originalFetch = global.fetch;
const mockFetch = jest.fn();

const respondWith = (data: unknown) => mockFetch.mockResolvedValue({
    ok: true,
    headers: new Headers({'Content-Type': 'application/json'}),
    json: async () => data,
});

describe('fetchRunApprovalGates', () => {
    beforeEach(() => {
        mockFetch.mockReset();
        global.fetch = mockFetch;
        setSiteUrl();
    });

    afterAll(() => {
        global.fetch = originalFetch;
        setSiteUrl();
    });

    it('uses the browser client to fetch the encoded run endpoint and carries the abort signal', async () => {
        respondWith([gate]);
        setSiteUrl('https://example.com/mattermost');
        const controller = new AbortController();

        await expect(fetchRunApprovalGates('run/1', controller.signal)).resolves.toEqual([gate]);
        expect(mockFetch).toHaveBeenCalledWith('/mattermost/plugins/playbooks/api/v0/runs/run%2F1/approval-gates', expect.objectContaining({
            method: 'get',
            signal: controller.signal,
            headers: expect.objectContaining({'X-Requested-With': 'XMLHttpRequest'}),
        }));
    });

    it.each([
        null,
        {},
        [{...gate, status: 'complete'}],
        [{...gate, can_review: 'true'}],
        [{...gate, path: 'https://example.com/request'}],
        [{...gate, path: 'javascript:alert(1)'}], // eslint-disable-line no-script-url
    ])('rejects an invalid response: %j', async (data) => {
        respondWith(data);

        await expect(fetchRunApprovalGates('run-1')).rejects.toThrow('Invalid approval gates response');
    });

    it('propagates network failures instead of returning an empty or completed approval', async () => {
        mockFetch.mockRejectedValue(new Error('Unavailable'));

        await expect(fetchRunApprovalGates('run-1')).rejects.toThrow('Unavailable');
    });

    it('propagates a denied response', async () => {
        mockFetch.mockResolvedValue({
            ok: false,
            status: 403,
            headers: new Headers({'Content-Type': 'application/json'}),
            json: async () => ({error: 'Forbidden'}),
        });

        await expect(fetchRunApprovalGates('run-1')).rejects.toThrow('Forbidden');
    });
});
