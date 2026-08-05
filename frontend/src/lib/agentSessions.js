// Pure helpers for the agent sessions list. Kept free of van and capi imports
// so they can be unit tested outside a browser.

export function sessionExpired(session, now = Date.now()) {
    const expiresAt = session?.expiresAt;
    if (!(expiresAt instanceof Date) || Number.isNaN(expiresAt.getTime())) return true;
    return expiresAt.getTime() <= now;
}

export function sessionLive(session, now = Date.now()) {
    return !session?.revoked && !sessionExpired(session, now);
}

// Live sessions first, then revoked and expired ones. The server already
// returns newest-first, so this is a stable partition rather than a re-sort:
// the newest live session ends up at the top.
export function orderSessions(sessions, now = Date.now()) {
    const live = [];
    const dead = [];
    for (const session of sessions || []) {
        (sessionLive(session, now) ? live : dead).push(session);
    }
    return [...live, ...dead];
}

export function sessionStatus(session, now = Date.now()) {
    // Revoked wins over expired: it is the more specific fact about why the
    // session stopped working.
    if (session?.revoked) return {label: "Revoked", tone: "text-gray-500"};
    if (sessionExpired(session, now)) return {label: "Expired", tone: "text-gray-500"};
    return {label: "Active", tone: "text-green-400"};
}
