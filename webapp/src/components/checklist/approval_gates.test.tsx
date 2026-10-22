// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';
import {
    act,
    cleanup,
    fireEvent,
    render,
    screen,
    waitFor,
} from '@testing-library/react';
import {IntlProvider} from 'react-intl';

import {fetchRunApprovalGates} from 'src/client';
import {ApprovalGate} from 'src/types/approval_gates';

import ApprovalGates from './approval_gates';

jest.mock('src/client', () => ({fetchRunApprovalGates: jest.fn()}));

const mockFetchGates = jest.mocked(fetchRunApprovalGates);
const pendingGate: ApprovalGate = {
    gate_id: 'gate-1',
    title: 'Budget approval',
    status: 'pending',
    path: '/office?page=%2Fwork%2Fapprovals%2Frequest-1',
    can_review: true,
};

const gatesView = (runID = 'run-1', updatedAt = 1) => (
    <IntlProvider locale='en'>
        <ApprovalGates
            key={runID}
            runID={runID}
            updatedAt={updatedAt}
        />
    </IntlProvider>
);

describe('Office approval gates', () => {
    beforeEach(() => {
        mockFetchGates.mockReset();
        mockFetchGates.mockResolvedValue([pendingGate]);
    });

    afterEach(() => {
        cleanup();
        jest.useRealTimers();
    });

    it('shows pending approvals as external Review links without checklist or decision controls', async () => {
        render(gatesView());

        expect((await screen.findByRole('link', {name: 'Review'})).getAttribute('href')).toBe(pendingGate.path);
        expect(screen.getByRole('heading', {name: 'Office approvals'})).toBeTruthy();
        expect(screen.getByText('Pending')).toBeTruthy();
        expect(screen.queryByRole('checkbox')).toBeNull();
        expect(screen.queryByRole('button', {name: /approve|reject/i})).toBeNull();
        expect(mockFetchGates).toHaveBeenCalledWith('run-1', expect.any(AbortSignal));
    });

    it('shows View request when the viewer cannot review a pending approval', async () => {
        mockFetchGates.mockResolvedValue([{...pendingGate, can_review: false}]);
        render(gatesView());

        expect((await screen.findByRole('link', {name: 'View request'})).getAttribute('href')).toBe(pendingGate.path);
        expect(screen.queryByRole('link', {name: 'Review'})).toBeNull();
    });

    it.each<{status: ApprovalGate['status']; label: string}>([
        {status: 'approved', label: 'Approved'},
        {status: 'rejected', label: 'Rejected'},
        {status: 'returned', label: 'Returned'},
        {status: 'cancelled', label: 'Cancelled'},
    ])('shows $label with a View request link', async ({status, label}) => {
        mockFetchGates.mockResolvedValue([{...pendingGate, status}]);
        render(gatesView());

        expect(await screen.findByText(label)).toBeTruthy();
        expect(screen.getByRole('link', {name: 'View request'})).toBeTruthy();
        expect(screen.queryByRole('link', {name: 'Review'})).toBeNull();
    });

    it('hides the section when the run has no approval gates', async () => {
        mockFetchGates.mockResolvedValue([]);
        render(gatesView());

        await waitFor(() => expect(screen.queryByRole('region', {name: 'Office approvals'})).toBeNull());
    });

    it('shows a recoverable error when loading fails', async () => {
        mockFetchGates.mockRejectedValueOnce(new Error('Unavailable'));
        render(gatesView());

        expect(await screen.findByRole('alert')).toBeTruthy();
        expect(screen.queryByText('Approved')).toBeNull();
        fireEvent.click(screen.getByRole('button', {name: 'Retry'}));

        expect(await screen.findByText('Pending')).toBeTruthy();
        expect(screen.queryByRole('alert')).toBeNull();
    });

    it('preserves the last known status and warns when a refresh fails', async () => {
        const view = render(gatesView());
        await screen.findByText('Pending');
        mockFetchGates.mockRejectedValueOnce(new Error('Unavailable'));

        view.rerender(gatesView('run-1', 2));

        expect((await screen.findByRole('alert')).textContent).toContain('Statuses may be out of date.');
        expect(screen.getByText('Pending')).toBeTruthy();
        expect(screen.queryByText('Approved')).toBeNull();

        mockFetchGates.mockResolvedValue([{...pendingGate, status: 'approved'}]);
        fireEvent.click(screen.getByRole('button', {name: 'Retry'}));

        expect(await screen.findByText('Approved')).toBeTruthy();
        expect(screen.queryByRole('alert')).toBeNull();
    });

    it('refreshes when returning from Office', async () => {
        render(gatesView());
        await screen.findByText('Pending');
        mockFetchGates.mockResolvedValue([{...pendingGate, status: 'returned'}]);

        fireEvent(window, new Event('focus'));

        expect(await screen.findByText('Returned')).toBeTruthy();
    });

    it('polls for external updates and stops polling and aborts requests on unmount', async () => {
        jest.useFakeTimers();
        const view = render(gatesView());
        await screen.findByText('Pending');
        mockFetchGates.mockResolvedValue([{...pendingGate, status: 'approved'}]);

        await act(async () => jest.advanceTimersByTime(30000));

        expect(screen.getByText('Approved')).toBeTruthy();
        expect(mockFetchGates).toHaveBeenCalledTimes(2);
        const signal = mockFetchGates.mock.calls[0][1];
        view.unmount();
        expect(signal?.aborted).toBe(true);
        await act(async () => jest.advanceTimersByTime(60000));
        fireEvent(window, new Event('focus'));
        expect(mockFetchGates).toHaveBeenCalledTimes(2);
    });

    it('ignores an old response after switching runs', async () => {
        let resolveOldRun: (gates: ApprovalGate[]) => void = jest.fn();
        mockFetchGates.mockReturnValueOnce(new Promise((resolve) => {
            resolveOldRun = resolve;
        }));
        const view = render(gatesView());
        mockFetchGates.mockResolvedValue([{...pendingGate, title: 'New run approval'}]);
        view.rerender(gatesView('run-2'));

        await screen.findByText('New run approval');
        await act(async () => resolveOldRun([{...pendingGate, status: 'approved'}]));

        expect(screen.queryByText('Budget approval')).toBeNull();
        expect(screen.queryByText('Approved')).toBeNull();
    });

    it('does not retain a previous run approval when loading the next run fails', async () => {
        const view = render(gatesView());
        await screen.findByText('Budget approval');
        mockFetchGates.mockRejectedValueOnce(new Error('Forbidden'));

        // ChecklistList keys ApprovalGates by run ID so its state cannot cross runs.
        view.rerender(gatesView('run-2'));

        expect(screen.queryByText('Budget approval')).toBeNull();
        expect(await screen.findByRole('alert')).toBeTruthy();
        expect(screen.queryByText('Budget approval')).toBeNull();
        expect(screen.queryByRole('link')).toBeNull();
    });
});
