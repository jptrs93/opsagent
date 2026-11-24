const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

export function createStreamStats(received = () => 0) {
    let base = 0;

    const stats = {
        connectedSince: null,
        lastHeartbeat: null,
        lastUpdate: null,
        updates: 0,
        seq: 0,
        get bytes() {
            return Math.max(0, received() - base);
        },
    };

    return {
        stats,
        opening() {
            base = received();
            stats.connectedSince = null;
            stats.lastHeartbeat = null;
            stats.lastUpdate = null;
            stats.updates = 0;
            stats.seq = 0;
        },
        connected(at = new Date()) {
            stats.connectedSince = at;
        },
        frame(message, at = new Date()) {
            stats.lastHeartbeat = at;
            const seq = Number(message?.seq || 0);
            if (seq > stats.seq) stats.seq = seq;
            if (message?.heartbeat) return;
            stats.lastUpdate = at;
            stats.updates += 1;
        },
        closed() {
            stats.connectedSince = null;
        },
    };
}

export function formatAge(at, now = new Date()) {
    if (!(at instanceof Date)) return "—";
    const delta = now.getTime() - at.getTime();
    if (delta < SECOND) return "just now";
    if (delta < MINUTE) return `${Math.floor(delta / SECOND)}s ago`;
    if (delta < HOUR) return `${Math.floor(delta / MINUTE)}m ago`;
    if (delta < DAY) return `${Math.floor(delta / HOUR)}h ago`;
    return `${Math.floor(delta / DAY)}d ago`;
}

export function formatByteCount(value) {
    const bytes = Math.max(0, Number(value) || 0);
    if (bytes < 1024) return `${Math.round(bytes)} B`;
    const units = ["KiB", "MiB", "GiB"];
    let scaled = bytes / 1024;
    let unit = 0;
    while (scaled >= 1024 && unit < units.length - 1) {
        scaled /= 1024;
        unit += 1;
    }
    return `${scaled.toFixed(1).replace(/\.0$/, "")} ${units[unit]}`;
}
