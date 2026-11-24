import van from "vanjs-core";
import {deploymentsStreamS, deploymentsStreamStats} from "../state/deployments.js";
import {formatAge, formatByteCount} from "../lib/streamStats.js";

const {div, span} = van.tags;

const TONES = {
    connected: {label: "Connected", dot: "bg-emerald-400", text: "text-gray-400"},
    connecting: {label: "Connecting", dot: "bg-gray-500 animate-pulse", text: "text-gray-500"},
    reconnecting: {label: "Reconnecting", dot: "bg-amber-400 animate-pulse", text: "text-amber-300"},
    offline: {label: "Offline", dot: "bg-red-400", text: "text-red-400"},
};

const toneOf = (status) => TONES[status] || TONES.offline;

// The stats change on every frame: read them off the clock, never from a
// binding. The card is position: fixed because the sidebar clips its overflow.
export function streamStatus() {
    const anchor = van.state(null);
    const now = van.state(new Date());
    let timer = null;

    const show = (event) => {
        const rect = event.currentTarget.getBoundingClientRect();
        now.val = new Date();
        anchor.val = {left: rect.left, bottom: window.innerHeight - rect.top + 6};
        if (!timer) timer = setInterval(() => { now.val = new Date(); }, 1000);
    };

    const hide = () => {
        anchor.val = null;
        if (timer) { clearInterval(timer); timer = null; }
    };

    const line = (label, value, testid) => div(
        {class: "flex items-center justify-between gap-4 py-px"},
        span({class: "text-gray-500"}, label),
        span({class: "tabular-nums text-gray-200", ...(testid ? {"data-testid": testid} : {})}, value),
    );

    const card = () => {
        const at = anchor.val;
        if (!at) return "";
        const stats = deploymentsStreamStats;
        const age = (read) => () => formatAge(read(), now.val);
        const ticking = (read) => () => { void now.val; return read(); };
        return div(
            {
                role: "tooltip",
                "data-testid": "stream-status-card",
                class: "pointer-events-none fixed z-[90] w-56 rounded-md border border-gray-600 bg-raised p-2 text-[11px] shadow-2xl",
                style: `left:${at.left}px;bottom:${at.bottom}px`,
            },
            () => {
                const stream = deploymentsStreamS.val;
                if (stream.status === "connected") return "";
                return div({class: "mb-1 border-b border-gray-700 pb-1"},
                    div({class: toneOf(stream.status).text}, stream.sentence),
                    stream.lastError ? div({class: "break-words text-gray-500"}, stream.lastError) : "");
            },
            line("Connected since", age(() => stats.connectedSince)),
            line("Last heartbeat", age(() => stats.lastHeartbeat)),
            line("Last update", age(() => stats.lastUpdate)),
            line("Total updates", ticking(() => stats.updates.toLocaleString("en-US")), "stream-status-updates"),
            line("Received", ticking(() => formatByteCount(stats.bytes))),
            line("Write sequence", ticking(() => stats.seq ? stats.seq.toLocaleString("en-US") : "—")),
        );
    };

    return div(
        {
            class: "flex min-w-0 flex-1 cursor-default items-center gap-2 py-1 text-xs",
            "data-testid": "stream-status",
            "data-status": () => deploymentsStreamS.val.status,
            tabindex: 0,
            onmouseenter: show,
            onmouseleave: hide,
            onfocus: show,
            onblur: hide,
        },
        span({class: () => `h-1.5 w-1.5 flex-none rounded-full ${toneOf(deploymentsStreamS.val.status).dot}`}),
        span({class: () => `truncate ${toneOf(deploymentsStreamS.val.status).text}`}, () => toneOf(deploymentsStreamS.val.status).label),
        card,
    );
}
