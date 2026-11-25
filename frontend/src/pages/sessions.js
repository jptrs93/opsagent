import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {agentSessionsS, userSessionsS} from "../state/deployments.js";
import {clearLoginState, loginS} from "../state/login.js";
import {checkIcon, copyIcon} from "../lib/icons.js";
import {formatHistoryTime} from "../lib/date.js";
import {
    agentPrompt,
    orderSessions,
    sessionExpiresAt,
    sessionPending,
    sessionStatus,
    sessionStoppable,
} from "../lib/agentSessions.js";
import {userSessionLive, userSessionStatus, summarizeUserAgent, isLinkSession} from "../lib/userSessions.js";
import {usersMapS} from "../state/deployments.js";
import {tabButton, tabStrip} from "../components/tabs.js";

const {button, div, p, span, table, tbody, td, th, thead, tr} = van.tags;

// Header cells stick to the top of the scrolling list, so they need an opaque
// background and an inset shadow in place of a bottom border, which a sticky
// cell in a border-separate table drops (styles/access.css).
const thCell = (label, extra = "") => th({class: `access-th ${extra}`}, label);
const tdCell = (extra, ...children) => td({class: `access-td ${extra}`}, ...children);

const statusCell = (status, text) => tdCell(`whitespace-nowrap ${status.tone}`,
    span({class: "inline-flex items-center gap-1.5"},
        span({class: "access-status-dot h-1.5 w-1.5 flex-none rounded-full bg-current"}),
        span({class: status.tone === "text-green-400" ? "access-status-ok" : ""}, text)));

const denseTable = (headers, bodyRows) => table(
    {class: "w-full border-separate border-spacing-0 text-xs"},
    thead(tr(...headers)),
    tbody(...bodyRows),
);

const emptyRow = (colCount, text) => tr(
    td({class: "access-td text-gray-500", colspan: colCount},
        div({class: "flex h-8 items-center justify-center text-xs"}, text)));

const rowAction = ({label, name = label, title = name, tone, disabled, onclick}) => button({
    type: "button",
    class: `access-action ${tone}`,
    disabled,
    title,
    "aria-label": name,
    onclick,
}, label);

function copyableText({value, testId}) {
    const copied = van.state(false);
    const copy = async () => {
        try {
            await navigator.clipboard.writeText(value());
            copied.val = true;
            setTimeout(() => { copied.val = false; }, 2000);
        } catch {
            copied.val = false;
        }
    };
    return div(
        {class: "access-copyable", "data-testid": testId},
        span({class: "access-copyable-text"}, value),
        button(
            {type: "button", class: "access-action access-action-quiet", onclick: () => { void copy(); }},
            () => copied.val ? checkIcon({class: "h-3 w-3 text-green-400"}) : copyIcon({class: "h-3 w-3"}),
            () => copied.val ? "Copied" : "Copy"),
    );
}

const errorLine = (error, testId) => () => error.val
    ? p({class: "px-2 pt-1.5 text-xs text-red-400 slate:px-3 graphite:px-3", "data-testid": testId}, error.val)
    : "";

function agentSessionsTab() {
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

    const approve = (session) => run(session.id, () => capi.postV1AgentSessionsApprove({sessionId: session.sessionId}));
    const stop = (session) => run(session.id, () => capi.postV1AgentSessionsRevoke({sessionId: session.sessionId}));

    const row = (session) => {
        const pending = sessionPending(session);
        const status = sessionStatus(session);
        const stoppable = sessionStoppable(session);
        const busy = () => busyID.val === session.id;
        return tr(
            {class: `access-tr ${pending ? "access-tr-pending" : ""}`},
            tdCell("whitespace-nowrap text-gray-300 tabular-nums", formatHistoryTime(session.createdAt)),
            statusCell(status,
                status.label === "Active" ? `Active until ${formatHistoryTime(sessionExpiresAt(session))}` : status.label),
            tdCell("whitespace-nowrap font-mono",
                pending
                    ? span({class: "rounded bg-code px-1.5 py-0.5 text-gray-100"}, session.approvalCode || "")
                    : span({class: "text-gray-600"}, "—")),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.requestingAddress || "unknown"),
            tdCell("w-px",
                div({class: "flex items-center justify-end gap-1.5"},
                    pending ? rowAction({
                        label: "Approve",
                        title: "Approve this request and let the agent collect its token",
                        tone: "access-action-primary",
                        disabled: busy,
                        onclick: () => { void approve(session); },
                    }) : "",
                    stoppable ? rowAction({
                        label: pending ? "Reject" : "Revoke",
                        name: pending ? "Reject this request" : "Revoke this session",
                        tone: "access-action-danger",
                        disabled: busy,
                        onclick: () => { void stop(session); },
                    }) : "",
                )),
        );
    };

    const promptText = () => agentPrompt(window.location.origin, loginS.val?.userId || 0);

    return div(
        {class: "flex flex-1 min-h-0 flex-col"},
        div(
            {class: "flex flex-none flex-col items-start gap-1.5 p-2 slate:px-3 slate:py-3 graphite:px-3 graphite:py-2.5"},
            p({class: "text-xs text-gray-400"},
                "Give this to an agent. It will fetch its own instructions and request a session, which you can approve below."),
            copyableText({value: promptText, testId: "agent-prompt"}),
        ),
        errorLine(error, "agent-session-error"),
        () => {
            const ordered = orderSessions(agentSessionsS.val);
            return div(
                {class: "app-scroll flex-1 min-h-0 overflow-y-auto", "data-testid": "agent-session-list"},
                denseTable(
                    [thCell("Started"), thCell("Status"), thCell("Approval code"), thCell("IP address"), thCell("", "w-px")],
                    ordered.length ? ordered.map(row) : [emptyRow(5, "No sessions yet")],
                ),
            );
        },
    );
}

function userSessionsTab() {
    const busyID = van.state("");
    const error = van.state("");
    const isCurrent = (session) => session.sessionId === (loginS.val?.sessionId || "");

    const revoke = async (session) => {
        busyID.val = session.id;
        error.val = "";
        try {
            await capi.postV1UserSessionsRevoke({sessionId: session.sessionId});
            if (isCurrent(session)) clearLoginState();
        } catch (e) {
            error.val = e.message || "Request failed";
        } finally {
            busyID.val = "";
        }
    };

    const userName = (id) => usersMapS.val.get(Number(id))?.name || `user ${id}`;
    const isMine = (session) => Number(session.userId || 0) === Number(loginS.val?.userId || 0);

    // A link row is a sign-in link this user issued for someone, or one
    // issued for this user: the device column says which, and the control
    // cancels it while it is unused.
    const row = (session) => {
        const status = userSessionStatus(session);
        const live = userSessionLive(session);
        const current = isCurrent(session);
        const link = isLinkSession(session);
        const device = link
            ? span({class: "text-gray-300"},
                span({class: "mr-2 rounded bg-amber-500/15 px-1.5 py-px text-[10px] font-medium text-amber-300", title: "A one-time sign-in link"}, "Link"),
                isMine(session) ? `for you, issued by ${session.issuedBy ? userName(session.issuedBy) : "the master password"}` : `for ${userName(session.userId)}`)
            : summarizeUserAgent(session.userAgent);
        return tr(
            {class: "access-tr", ...(link ? {"data-testid": `user-session-link-${session.sessionId}`} : {})},
            tdCell("whitespace-nowrap text-gray-300 tabular-nums", formatHistoryTime(session.createdAt)),
            statusCell(status, status.label),
            tdCell("whitespace-nowrap text-gray-300",
                device,
                current
                    ? span({class: "ml-2 rounded bg-brand/15 px-1.5 py-px text-[10px] font-medium text-blue-300"}, "This browser")
                    : ""),
            tdCell("whitespace-nowrap font-mono text-gray-500", session.requestingAddress || "unknown"),
            tdCell("whitespace-nowrap text-gray-400 tabular-nums", formatHistoryTime(session.expiresAt)),
            tdCell("w-px",
                div({class: "flex items-center justify-end"},
                    live ? rowAction({
                        label: link ? "Cancel" : (current ? "Sign out" : "Revoke"),
                        name: link ? "Cancel this link" : (current ? "Sign out this browser" : "Revoke this session"),
                        tone: "access-action-danger",
                        disabled: () => busyID.val === session.id,
                        onclick: () => { void revoke(session); },
                    }) : "",
                )),
        );
    };

    return div(
        {class: "flex flex-1 min-h-0 flex-col"},
        errorLine(error, "user-session-error"),
        () => div(
            {class: "app-scroll flex-1 min-h-0 overflow-y-auto", "data-testid": "user-session-list"},
            denseTable(
                [thCell("Signed in"), thCell("Status"), thCell("Device"), thCell("IP address"),
                    thCell("Expires"), thCell("", "w-px")],
                userSessionsS.val.length ? userSessionsS.val.map(row) : [emptyRow(6, "No sessions yet")],
            ),
        ),
    );
}

export function sessionsPage() {
    const tab = van.state("agent");

    return div(
        {class: "page"},
        tabStrip({label: "Sessions"},
            tabButton({id: "agent", activeTab: tab, title: "Agent sessions", testid: "sessions-tab-agent", count: () => String(agentSessionsS.val.length)}),
            tabButton({id: "user", activeTab: tab, title: "User sessions", testid: "sessions-tab-user", count: () => String(userSessionsS.val.length)}),
        ),
        // The tab body is a flex column, not a scroller: each tab owns its
        // own scrolling list so the table header stays fixed.
        div(
            {class: "page-body flex flex-col overflow-hidden"},
            () => tab.val === "agent" ? agentSessionsTab() : userSessionsTab(),
        ),
    );
}
