// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

export interface ApprovalGate {
    gate_id: string;
    title: string;
    status: 'pending' | 'approved' | 'rejected' | 'returned' | 'cancelled';
    path: string;
    can_review: boolean;
}
