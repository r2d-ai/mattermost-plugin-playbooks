// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React, {useEffect, useState} from 'react';
import {defineMessages, useIntl} from 'react-intl';
import styled from 'styled-components';

import {fetchRunApprovalGates} from 'src/client';
import {ApprovalGate} from 'src/types/approval_gates';

interface Props {
    runID: string;
    updatedAt: number;
}

type GatesState =
    | {kind: 'loading'}
    | {kind: 'ready'; gates: ApprovalGate[]}
    | {kind: 'error'; gates: ApprovalGate[]};

const statusMessages = defineMessages({
    pending: {defaultMessage: 'Pending'},
    approved: {defaultMessage: 'Approved'},
    rejected: {defaultMessage: 'Rejected'},
    returned: {defaultMessage: 'Returned'},
    cancelled: {defaultMessage: 'Cancelled'},
});

const ApprovalGates = ({runID, updatedAt}: Props) => {
    const {formatMessage} = useIntl();
    const [state, setState] = useState<GatesState>({kind: 'loading'});
    const [retryCount, setRetryCount] = useState(0);

    useEffect(() => {
        const controller = new AbortController();
        let inFlight = false;

        const refresh = async () => {
            if (inFlight) {
                return;
            }
            inFlight = true;

            try {
                const gates = await fetchRunApprovalGates(runID, controller.signal);
                if (!controller.signal.aborted) {
                    setState({kind: 'ready', gates});
                }
            } catch {
                if (!controller.signal.aborted) {
                    setState((previous) => ({
                        kind: 'error',
                        gates: previous.kind === 'loading' ? [] : previous.gates,
                    }));
                }
            } finally {
                inFlight = false;
            }
        };

        refresh();

        // Office updates can arrive without a run update; refresh after returning
        // to this window and periodically while the run remains open.
        const interval = window.setInterval(refresh, 30000);
        window.addEventListener('focus', refresh);

        return () => {
            controller.abort();
            window.clearInterval(interval);
            window.removeEventListener('focus', refresh);
        };
    }, [runID, updatedAt, retryCount]);

    if (state.kind === 'ready' && state.gates.length === 0) {
        return null;
    }

    const gates = state.kind === 'loading' ? [] : state.gates;

    return (
        <Section aria-label={formatMessage({defaultMessage: 'Office approvals'})}>
            <Heading>{formatMessage({defaultMessage: 'Office approvals'})}</Heading>
            <Description>{formatMessage({defaultMessage: 'Approval decisions are managed in Office.'})}</Description>
            {state.kind === 'loading' && (
                <Description role='status'>
                    {formatMessage({defaultMessage: 'Loading approvals…'})}
                </Description>
            )}
            {state.kind === 'error' && (
                <ErrorNotice role='alert'>
                    <span>
                        {gates.length > 0 ?
                            formatMessage({defaultMessage: 'Could not update approvals. Statuses may be out of date.'}) :
                            formatMessage({defaultMessage: 'Could not load approvals.'})}
                    </span>
                    <RetryButton
                        type='button'
                        onClick={() => setRetryCount((count) => count + 1)}
                    >
                        {formatMessage({defaultMessage: 'Retry'})}
                    </RetryButton>
                </ErrorNotice>
            )}
            {gates.length > 0 && (
                <GateList>
                    {gates.map((gate) => (
                        <GateRow key={gate.gate_id}>
                            <GateTitle>{gate.title}</GateTitle>
                            <GateDetails>
                                <StatusBadge $status={gate.status}>
                                    {formatMessage(statusMessages[gate.status])}
                                </StatusBadge>
                                <a
                                    href={gate.path}
                                    target='_blank'
                                    rel='noopener noreferrer'
                                >
                                    {gate.status === 'pending' && gate.can_review ?
                                        formatMessage({defaultMessage: 'Review'}) :
                                        formatMessage({defaultMessage: 'View request'})}
                                </a>
                            </GateDetails>
                        </GateRow>
                    ))}
                </GateList>
            )}
        </Section>
    );
};

const Section = styled.section`
    margin: 16px 0;
    padding: 12px;
    border: 1px solid var(--center-channel-color-16);
    border-radius: 4px;
`;

const Heading = styled.h3`
    margin: 0 0 4px;
    font-size: 14px;
    font-weight: 600;
    line-height: 20px;
`;

const Description = styled.p`
    margin: 0;
    color: var(--center-channel-color-64);
    font-size: 12px;
    line-height: 18px;
`;

const GateList = styled.ul`
    padding: 0;
    margin: 12px 0 0;
    list-style: none;
`;

const GateRow = styled.li`
    padding: 10px 0;
    border-top: 1px solid var(--center-channel-color-08);

    &:last-child {
        padding-bottom: 0;
    }
`;

const GateTitle = styled.div`
    margin-bottom: 6px;
    overflow-wrap: anywhere;
    font-weight: 600;
`;

const GateDetails = styled.div`
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 12px;
`;

const StatusBadge = styled.span<{$status: ApprovalGate['status']}>`
    padding: 2px 6px;
    border-radius: 4px;
    background: var(--center-channel-color-08);
    color: var(--center-channel-color);
    font-weight: 600;

    ${({$status}) => $status === 'approved' && `
        background: rgba(var(--online-indicator-rgb), 0.08);
        color: var(--online-indicator);
    `}

    ${({$status}) => $status === 'rejected' && `
        background: rgba(var(--dnd-indicator-rgb), 0.08);
        color: var(--dnd-indicator);
    `}
`;

const ErrorNotice = styled.div`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    font-size: 12px;
`;

const RetryButton = styled.button`
    padding: 0;
    border: 0;
    background: none;
    color: var(--link-color);
    font-weight: 600;
`;

export default ApprovalGates;
