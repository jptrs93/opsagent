import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {agentSessionsS} from "../state/deployments.js";
import {clearLoginState, loginS} from "../state/login.js";
import {codeBlock} from "../components/codeBlock.js";
import {xIcon} from "../lib/icons.js";
import {formatHistoryTime} from "../lib/date.js";
import {
    agentPrompt,
    orderSessions,
    sessionPending,
    sessionStatus,
    sessionStoppable,
} from "../lib/agentSessions.js";
import {personalSessionLive, personalSessionStatus, summarizeUserAgent} from "../lib/personalSessions.js";

const {button, div, p, span, table, tbody, td, th, thead, tr} = van.tags;

const iconButtonClass = "rounded p-1 text-gray-400 transition-colors hover:bg-gray-700 disabled:cursor-not-allowed disabled:opacity-25 disabled:hover:bg-transparent cursor-pointer";

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
        div({class: "flex h-8 items-center justify-center text-xs"}, text)));

const errorLine = (error, testId) => () => error.val
    ? p({class: "px-2 pt-1.5 text-xs text-red-400", "data-testid": testId}, error.val)
    : "";

function agentSessionsTab() {
    const promptOpen = van.state(true);
    const error = van.state("");
    const busyID = van.state("");

    const run = async (id, fn) => {
        busyID.val = id;
        error.val = "";
        try {
            await fn();
        } catch (e) {
            error.val = e.message || "Request failed";
        } finally {
            busyID.val = "";
        }
    };

    const approve = (session) => run(session.id, () => capi.postV1AgentSessionsApprove({id: session.id}));
    const stop = (session) => run(session.id, () => capi.postV1AgentSessionsRevoke({id: session.id}));

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
                    ? span({class: "rounded bg-code px-1.5 py-0.5 text-gray-100"}, session.approvalCode || "")
                    : span({class: "text-gray-600"}, "—")),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.requestingAddress || "unknown"),
            tdCell("w-px",
                div({class: "flex items-center justify-end gap-1"},
                    pending ? button({
                        type: "button",
                        class: "btn-primary text-xs py-0.5 px-2 shrink-0 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed",
                        disabled: () => busyID.val === session.id,
                        title: "Approve this request and let the agent collect its token",
                        onclick: () => { void approve(session); },
                    }, "Approve") : "",
                    button({
                        type: "button",
                        class: `${iconButtonClass} hover:text-red-400`,
                        disabled: () => !stoppable || busyID.val === session.id,
                        title: stoppable
                            ? (pending ? "Reject this request" : "Revoke this session")
                            : `Session is already ${status.label.toLowerCase()}`,
                        "aria-label": pending ? "Reject this request" : "Revoke this session",
                        onclick: () => { void stop(session); },
                    }, xIcon({size: 13})),
                )),
        );
    };

    const promptText = () => agentPrompt(window.location.origin, loginS.val?.userId || 0);

    return div(
        {class: "flex flex-col"},
        div(
            {class: "flex flex-col gap-1.5 p-2"},
            p({class: "text-[11px] text-gray-500"},
                "Give this to an agent. It will fetch its own instructions and request a session, which you can approve below."),
            codeBlock({
                title: "Agent prompt",
                value: promptText,
                open: promptOpen,
                wrap: true,
                lineNumbers: false,
                testId: "agent-prompt",
            }),
        ),
        errorLine(error, "agent-session-error"),
        () => {
            const ordered = orderSessions(agentSessionsS.val);
            return div(
                {"data-testid": "agent-session-list"},
                denseTable(
                    [thCell("Started"), thCell("Status"), thCell("Approval code"), thCell("Origin"), thCell("", "w-px")],
                    ordered.length ? ordered.map(row) : [emptyRow(5, "No sessions yet")],
                ),
            );
        },
    );
}

function personalSessionsTab({sessionsS, error, reload}) {
    const busyID = van.state("");

    const revoke = async (session) => {
        busyID.val = session.id;
        error.val = "";
        try {
            await capi.postV1PersonalSessionsRevoke({id: session.id});
            if (session.current) {
                clearLoginState();
                return;
            }
            await reload();
        } catch (e) {
            error.val = e.message || "Request failed";
        } finally {
            busyID.val = "";
        }
    };

    const row = (session) => {
        const status = personalSessionStatus(session);
        const live = personalSessionLive(session);
        return tr(
            {class: "hover:bg-gray-800/40"},
            tdCell("whitespace-nowrap text-gray-300 tabular-nums", formatHistoryTime(session.createdAt)),
            tdCell(`whitespace-nowrap ${status.tone}`, status.label),
            tdCell("whitespace-nowrap text-gray-300",
                summarizeUserAgent(session.userAgent),
                session.current
                    ? span({class: "ml-2 rounded bg-brand/15 px-1.5 py-px text-[10px] font-medium text-blue-300"}, "This browser")
                    : ""),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.requestingAddress || "unknown"),
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
                        onclick: () => { void revoke(session); },
                    }, xIcon({size: 13})),
                )),
        );
    };

    return div(
        {class: "flex flex-col"},
        errorLine(error, "personal-session-error"),
        () => div(
            {"data-testid": "personal-session-list"},
            denseTable(
                [thCell("Signed in"), thCell("Status"), thCell("Device"), thCell("IP address"),
                    thCell("Last active"), thCell("Expires"), thCell("", "w-px")],
                sessionsS.val.length ? sessionsS.val.map(row) : [emptyRow(7, "No sessions yet")],
            ),
        ),
    );
}

export function sessionsPage() {
    const tab = van.state("agent");
    const personalSessionsS = van.state([]);
    const personalError = van.state("");

    const loadPersonalSessions = async () => {
        try {
            const res = await capi.postV1PersonalSessionsList();
            personalSessionsS.val = res.items || [];
            personalError.val = "";
        } catch (e) {
            personalError.val = e.message || "Failed to load sessions";
        }
    };
    void loadPersonalSessions();

    const tabButton = (key, label, countS) => button(
        {
            type: "button",
            role: "tab",
            "aria-selected": () => String(tab.val === key),
            class: () => `-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors ${tab.val === key
                ? "border-brand text-gray-100"
                : "border-transparent text-gray-400 hover:text-gray-200"}`,
            onclick: () => {
                tab.val = key;
                if (key === "personal") void loadPersonalSessions();
            },
        },
        label,
        () => span({class: "text-[10px] text-gray-500 tabular-nums"}, String(countS())),
    );

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        div(
            {class: "flex flex-none items-end gap-1 border-b border-gray-800 bg-gray-950/40 px-2 pt-1", role: "tablist"},
            tabButton("agent", "Agent sessions", () => agentSessionsS.val.length),
            tabButton("personal", "Personal sessions", () => personalSessionsS.val.length),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-y-auto"},
            () => tab.val === "agent"
                ? agentSessionsTab()
                : personalSessionsTab({sessionsS: personalSessionsS, error: personalError, reload: loadPersonalSessions}),
        ),
    );
}
