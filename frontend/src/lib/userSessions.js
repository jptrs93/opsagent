// Pure helpers for the user sessions list. Kept free of van and capi
// imports so they can be unit tested outside a browser.

// UserSessionKind on the wire: 0 is a full session; 1 a root session opened
// with the master password, which has no user and may only enrol or recover
// users; 2 an enrolment or recovery link, never a bearer, redeemed once.
export const USER_SESSION_KIND_FULL = 0;
export const USER_SESSION_KIND_ROOT = 1;
export const USER_SESSION_KIND_LINK = 2;

export const isRootSession = (session) => Number(session?.kind || 0) === USER_SESSION_KIND_ROOT;
export const isLinkSession = (session) => Number(session?.kind || 0) === USER_SESSION_KIND_LINK;

const validTime = (value) => value instanceof Date && value.getTime() > 0;

export function userSessionStatus(session, now = Date.now()) {
    if (validTime(session?.revokedAt)) return {label: isLinkSession(session) ? "Cancelled" : "Revoked", tone: "text-gray-500"};
    if (isLinkSession(session) && validTime(session?.redeemedAt)) return {label: "Redeemed", tone: "text-gray-400"};
    if (validTime(session?.expiresAt) && session.expiresAt.getTime() <= now) {
        return {label: "Expired", tone: "text-gray-500"};
    }
    return isLinkSession(session) ? {label: "Unused", tone: "text-amber-300"} : {label: "Active", tone: "text-green-400"};
}

export function userSessionLive(session, now = Date.now()) {
    const label = userSessionStatus(session, now).label;
    return label === "Active" || label === "Unused";
}

export function summarizeUserAgent(ua) {
    if (!ua) return "Unknown device";
    const os = /iPhone/.test(ua) ? "iPhone"
        : /iPad/.test(ua) ? "iPad"
            : /Android/.test(ua) ? "Android"
                : /Mac OS X|Macintosh/.test(ua) ? "macOS"
                    : /Windows/.test(ua) ? "Windows"
                        : /Linux|X11/.test(ua) ? "Linux"
                            : "";
    let browser = "";
    let match;
    if ((match = /Edg(?:e|A|iOS)?\/(\d+)/.exec(ua))) browser = `Edge ${match[1]}`;
    else if ((match = /OPR\/(\d+)/.exec(ua))) browser = `Opera ${match[1]}`;
    else if ((match = /Firefox\/(\d+)/.exec(ua))) browser = `Firefox ${match[1]}`;
    else if ((match = /Chrome\/(\d+)/.exec(ua))) browser = `Chrome ${match[1]}`;
    else if (/Safari\//.test(ua)) {
        const version = /Version\/(\d+)/.exec(ua);
        browser = version ? `Safari ${version[1]}` : "Safari";
    }
    if (browser && os) return `${browser} · ${os}`;
    return browser || os || ua.slice(0, 40);
}
