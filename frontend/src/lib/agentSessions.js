// Pure helpers for the agent sessions list. Kept free of van and capi imports
// so they can be unit tested outside a browser.

// Mirrors the AgentSessionStatus enum in api-contract/model.proto. Duplicated
// rather than imported because the generated capi module reaches for `window`.
export const STATUS = {
    UNKNOWN: 0,
    PENDING: 1,
    APPROVED: 2,
    REJECTED: 3,
    REVOKED: 4,
};

export function sessionPending(session) {
    return session?.status === STATUS.PENDING;
}

// A pending or uncollected session has no expiry yet, so "no expiry" cannot
// mean expired: only an approved session with a date in the past has run out.
export function sessionExpired(session, now = Date.now()) {
    const expiresAt = session?.expiresAt;
    if (!(expiresAt instanceof Date) || Number.isNaN(expiresAt.getTime()) || expiresAt.getTime() === 0) {
        return false;
    }
    return expiresAt.getTime() <= now;
}

export function sessionLive(session, now = Date.now()) {
    return session?.status === STATUS.APPROVED && !sessionExpired(session, now);
}

// Pending requests first because they are the only rows that need an action,
// then live sessions, then everything finished. The server already returns
// newest-first, so this is a stable partition rather than a re-sort.
export function orderSessions(sessions, now = Date.now()) {
    const pending = [];
    const live = [];
    const dead = [];
    for (const session of sessions || []) {
        if (sessionPending(session)) pending.push(session);
        else if (sessionLive(session, now)) live.push(session);
        else dead.push(session);
    }
    return [...pending, ...live, ...dead];
}

export function sessionStatus(session, now = Date.now()) {
    switch (session?.status) {
        case STATUS.PENDING:
            return {label: "Awaiting approval", tone: "text-amber-400"};
        case STATUS.REJECTED:
            return {label: "Rejected", tone: "text-gray-500"};
        case STATUS.REVOKED:
            return {label: "Revoked", tone: "text-gray-500"};
        case STATUS.APPROVED:
            if (sessionExpired(session, now)) return {label: "Expired", tone: "text-gray-500"};
            // Approved but never picked up. The distinction matters: the
            // operator did their part and the agent never came back.
            if (!session?.expiresAt || session.expiresAt.getTime() === 0) {
                return {label: "Approved, not collected", tone: "text-amber-400"};
            }
            return {label: "Active", tone: "text-green-400"};
        default:
            return {label: "Unknown", tone: "text-gray-500"};
    }
}

// Both a pending request and a live session can be stopped; a finished one
// cannot. The label differs because rejecting a request the operator never made
// is a different act from cutting off a session they approved.
export function sessionStoppable(session, now = Date.now()) {
    return sessionPending(session) || sessionLive(session, now);
}
