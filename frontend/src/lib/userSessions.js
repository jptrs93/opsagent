// Pure helpers for the user sessions list. Kept free of van and capi
// imports so they can be unit tested outside a browser.

// UserSessionKind on the wire: 0 is a full session, 1 a bootstrap session
// opened by master-password exchange that may only register a passkey.
export const USER_SESSION_KIND_BOOTSTRAP = 1;

export const isBootstrapSession = (session) => Number(session?.kind || 0) === USER_SESSION_KIND_BOOTSTRAP;

const validTime = (value) => value instanceof Date && value.getTime() > 0;

export function userSessionStatus(session, now = Date.now()) {
    if (validTime(session?.revokedAt)) return {label: "Revoked", tone: "text-gray-500"};
    if (validTime(session?.expiresAt) && session.expiresAt.getTime() <= now) {
        return {label: "Expired", tone: "text-gray-500"};
    }
    return {label: "Active", tone: "text-green-400"};
}

export function userSessionLive(session, now = Date.now()) {
    return userSessionStatus(session, now).label === "Active";
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
