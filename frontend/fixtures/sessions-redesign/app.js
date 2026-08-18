import van from "vanjs-core";
import {codeBlock} from "/src/components/codeBlock.js";
import {xIcon} from "/src/lib/icons.js";
import {formatHistoryTime} from "/src/lib/date.js";
import {
    STATUS,
    orderSessions,
    sessionPending,
    sessionStatus,
    sessionStoppable,
} from "/src/lib/agentSessions.js";
import {scenarios} from "./mockData.js";

const {button, div, h1, header, main, p, span, table, tbody, td, th, thead, tr} = van.tags;

// ---------------------------------------------------------------------------
// Proposed page. Everything inside sessionsPageProposal is what would move to
// src/pages/sessions.js (and a personalSessions component) if the design is
// accepted; the fixture chrome below it stays here.
// ---------------------------------------------------------------------------

const MOCK_PROMPT = "Fetch instructions for using our deployment orchestration platform, opendeploy from https://deploy.example.com/v1/agent-sessions/instructions?user_id=1. Then request a new session if you don't have an existing valid token.";

const iconButtonClass = "rounded p-1 text-gray-400 transition-colors hover:bg-gray-700 disabled:cursor-not-allowed disabled:opacity-25 disabled:hover:bg-transparent cursor-pointer";

// Excel-ish cells: tight padding, hairline row and column rules. The column
// rules are one shade fainter than the row rules so the grid reads as texture
// rather than boxes.
const thCell = (label, extra = "") => th(
    {class: `border-b border-gray-700/70 border-r border-r-gray-800/40 last:border-r-0 bg-gray-950/40 px-2 py-1 text-left text-[10px] font-medium uppercase tracking-wide text-gray-500 ${extra}`},
    label);
const tdCell = (extra, ...children) => td(
    {class: `border-b border-gray-800/50 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] ${extra}`},
    ...children);

const denseTable = (headers, bodyRows) => table(
    {class: "w-full border-collapse text-xs"},
    thead(tr(...headers)),
    tbody(...bodyRows),
);

const emptyRow = (colCount, text) => tr(
    td({class: "border-b border-gray-800/50 px-2 py-[3px] text-gray-500", colspan: colCount},
        div({class: "flex h-8 items-center justify-center"}, text)));

// --- Agent sessions tab ----------------------------------------------------

function agentSessionsTab({sessionsS, onApprove, onRevoke}) {
    const promptOpen = van.state(true);
    const busyID = van.state("");

    const run = async (id, fn) => {
        busyID.val = id;
        try { await fn(); } finally { busyID.val = ""; }
    };

    const row = (session) => {
        const pending = sessionPending(session);
        const status = sessionStatus(session);
        const stoppable = sessionStoppable(session);
        return tr(
            {class: pending ? "bg-amber-500/5" : "hover:bg-gray-800/40"},
            tdCell("whitespace-nowrap text-gray-300 tabular-nums", formatHistoryTime(session.createdAt)),
            tdCell(`whitespace-nowrap ${status.tone}`,
                status.label === "Active" ? `Active until ${formatHistoryTime(session.expiresAt)}` : status.label),
            tdCell("whitespace-nowrap font-mono",
                pending
                    ? span({class: "rounded bg-code px-1.5 py-0.5 text-gray-100"}, session.approvalCode)
                    : span({class: "text-gray-600"}, "—")),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.requestingAddress || "unknown"),
            tdCell("w-px",
                div({class: "flex items-center justify-end gap-1"},
                    pending ? button({
                        type: "button",
                        class: "btn-primary text-xs py-0.5 px-2 shrink-0 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed",
                        disabled: () => busyID.val === session.id,
                        title: "Approve this request and let the agent collect its token",
                        onclick: () => { void run(session.id, () => onApprove(session)); },
                    }, "Approve") : "",
                    button({
                        type: "button",
                        class: `${iconButtonClass} hover:text-red-400`,
                        disabled: () => !stoppable || busyID.val === session.id,
                        title: stoppable
                            ? (pending ? "Reject this request" : "Revoke this session")
                            : `Session is already ${status.label.toLowerCase()}`,
                        "aria-label": pending ? "Reject this request" : "Revoke this session",
                        onclick: () => { void run(session.id, () => onRevoke(session)); },
                    }, xIcon({size: 13})),
                )),
        );
    };

    return div(
        {class: "flex flex-col"},
        div(
            {class: "flex flex-col gap-1.5 p-2"},
            p({class: "text-[11px] text-gray-500"},
                "Give this to an agent. It will fetch its own instructions and request a session, which you can approve below."),
            codeBlock({
                title: "Agent prompt",
                value: MOCK_PROMPT,
                open: promptOpen,
                wrap: true,
                lineNumbers: false,
                testId: "agent-prompt",
            }),
        ),
        () => {
            const ordered = orderSessions(sessionsS.val);
            return denseTable(
                [thCell("Started"), thCell("Status"), thCell("Approval code"), thCell("Origin"), thCell("", "w-px")],
                ordered.length ? ordered.map(row) : [emptyRow(5, "No sessions yet")],
            );
        },
    );
}

// --- Personal sessions tab -------------------------------------------------

// Would live next to sessionStatus in src/lib once the backend models exist.
function personalStatus(session, now = Date.now()) {
    if (session.revoked) return {label: "Revoked", tone: "text-gray-500"};
    if (session.expiresAt.getTime() <= now) return {label: "Expired", tone: "text-gray-500"};
    return {label: "Active", tone: "text-green-400"};
}

function personalSessionsTab({sessionsS, onRevoke}) {
    const busyID = van.state("");

    const row = (session) => {
        const status = personalStatus(session);
        const live = status.label === "Active";
        return tr(
            {class: "hover:bg-gray-800/40"},
            tdCell("whitespace-nowrap text-gray-300 tabular-nums", formatHistoryTime(session.createdAt)),
            tdCell(`whitespace-nowrap ${status.tone}`, status.label),
            tdCell("whitespace-nowrap text-gray-300",
                session.device,
                session.current
                    ? span({class: "ml-2 rounded bg-brand/15 px-1.5 py-px text-[10px] font-medium text-blue-300"}, "This browser")
                    : ""),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.ip),
            tdCell("whitespace-nowrap text-gray-400 tabular-nums", formatHistoryTime(session.lastActiveAt)),
            tdCell("whitespace-nowrap text-gray-400 tabular-nums", formatHistoryTime(session.expiresAt)),
            tdCell("w-px",
                div({class: "flex items-center justify-end"},
                    button({
                        type: "button",
                        class: `${iconButtonClass} hover:text-red-400`,
                        disabled: () => !live || busyID.val === session.id,
                        title: !live
                            ? `Session is already ${status.label.toLowerCase()}`
                            : (session.current ? "Sign out this browser" : "Revoke this session"),
                        "aria-label": session.current ? "Sign out this browser" : "Revoke this session",
                        onclick: async () => {
                            busyID.val = session.id;
                            try { await onRevoke(session); } finally { busyID.val = ""; }
                        },
                    }, xIcon({size: 13})),
                )),
        );
    };

    return div(
        {class: "flex flex-col"},
        () => {
            const sessions = sessionsS.val;
            return denseTable(
                [thCell("Signed in"), thCell("Status"), thCell("Device"), thCell("IP address"),
                    thCell("Last active"), thCell("Expires"), thCell("", "w-px")],
                sessions.length ? sessions.map(row) : [emptyRow(7, "No sessions yet")],
            );
        },
    );
}

// --- Page frame ------------------------------------------------------------

function sessionsPageProposal(props) {
    const tab = van.state("agent");

    const tabButton = (key, label, countS) => button(
        {
            type: "button",
            role: "tab",
            "aria-selected": () => String(tab.val === key),
            class: () => `-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors ${tab.val === key
                ? "border-brand text-gray-100"
                : "border-transparent text-gray-400 hover:text-gray-200"}`,
            onclick: () => { tab.val = key; },
        },
        label,
        () => span({class: "text-[10px] text-gray-500 tabular-nums"}, String(countS())),
    );

    return div(
        // Same flush frame as the IAM and nodes pages: one surface running to
        // the window edges, no card and no padding gap around the content.
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        div(
            {class: "flex flex-none items-end gap-1 border-b border-gray-800 bg-gray-950/40 px-2 pt-1", role: "tablist"},
            tabButton("agent", "Agent sessions", () => props.agentSessionsS.val.length),
            tabButton("personal", "Personal sessions", () => props.personalSessionsS.val.length),
        ),
        div(
            // No app-scroll: its stable scrollbar gutter would hold the bands
            // and table rules 8px off the right edge (same as the flush pages).
            {class: "flex-1 min-h-0 overflow-y-auto"},
            () => tab.val === "agent"
                ? agentSessionsTab({sessionsS: props.agentSessionsS, onApprove: props.onApprove, onRevoke: props.onRevokeAgent})
                : personalSessionsTab({sessionsS: props.personalSessionsS, onRevoke: props.onRevokePersonal}),
        ),
    );
}

// ---------------------------------------------------------------------------
// Fixture chrome: scenario switcher + mock mutation handlers.
// ---------------------------------------------------------------------------

const scenario = van.state("typical");
const pageHost = div({class: "contents"});

function buildPage() {
    const data = scenarios[scenario.val]();
    const agentSessionsS = van.state(data.agentSessions);
    const personalSessionsS = van.state(data.personalSessions);

    const patchAgent = (id, patch) => {
        agentSessionsS.val = agentSessionsS.val.map(s => s.id === id ? {...s, ...patch} : s);
    };

    return sessionsPageProposal({
        agentSessionsS,
        personalSessionsS,
        onApprove: (session) => {
            // Approve leaves the session uncollected; a moment later the mock
            // agent "collects" its token, which is when the expiry appears.
            patchAgent(session.id, {status: STATUS.APPROVED, approvalCode: "", expiresAt: new Date(0)});
            setTimeout(() => patchAgent(session.id, {expiresAt: new Date(Date.now() + 12 * 3600 * 1000)}), 2500);
        },
        onRevokeAgent: (session) => {
            patchAgent(session.id, {
                status: sessionPending(session) ? STATUS.REJECTED : STATUS.REVOKED,
                approvalCode: "",
            });
        },
        onRevokePersonal: (session) => {
            personalSessionsS.val = personalSessionsS.val.map(s => s.id === session.id ? {...s, revoked: true} : s);
        },
    });
}

const renderPage = () => pageHost.replaceChildren(buildPage());

const controls = div(
    {class: "flex flex-wrap items-center gap-3"},
    div(
        {class: "inline-flex rounded-md border border-gray-800 bg-gray-900 p-0.5"},
        ...[["typical", "Typical"], ["many", "Many"], ["empty", "Empty"]].map(([value, text]) => button({
            type: "button",
            class: () => `rounded px-3 py-1.5 text-xs transition-colors cursor-pointer ${scenario.val === value
                ? "bg-gray-700 text-white"
                : "text-gray-400 hover:text-gray-200"}`,
            "aria-pressed": () => String(scenario.val === value),
            onclick: () => {
                if (scenario.val === value) return;
                scenario.val = value;
                renderPage();
            },
        }, text)),
    ),
    button({
        type: "button",
        class: "btn-secondary py-1.5 px-3 text-xs",
        onclick: renderPage,
    }, "Reset"),
);

van.add(document.body,
    div(
        {class: "flex h-full min-h-0 flex-col"},
        header(
            {class: "shrink-0 border-b border-gray-800 bg-gray-950/85 px-4 py-3"},
            div(
                {class: "mx-auto flex max-w-[1400px] flex-col justify-between gap-3 xl:flex-row xl:items-end"},
                div(
                    h1({class: "text-lg font-semibold text-white"}, "Sessions page redesign fixture"),
                    p({class: "mt-1 text-xs text-gray-500"},
                        "Proposal: flush two-tab page (Agent / Personal), dense gridline tables. Approve a pending row to watch it collect."),
                ),
                controls,
            ),
        ),
        main(
            {class: "mx-auto flex min-h-0 w-full max-w-[1400px] flex-1 flex-col overflow-hidden p-4"},
            div({class: "flex min-h-0 flex-1 flex-col overflow-hidden rounded border border-gray-800"}, pageHost),
        ),
    ),
);

renderPage();
