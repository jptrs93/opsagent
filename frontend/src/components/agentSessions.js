import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {agentSessionsS} from "../state/deployments.js";
import {loginS} from "../state/login.js";
import {checkIcon, copyIcon, xIcon} from "../lib/icons.js";
import {formatHistoryTime} from "../lib/date.js";
import {
    orderSessions,
    sessionPending,
    sessionStatus,
    sessionStoppable,
} from "../lib/agentSessions.js";

const {button, code, div, h2, p, span} = van.tags;

export const INSTRUCTIONS_PATH = "/v1/agent-sessions/instructions";

// The one line an operator hands to an agent. The API is served from the same
// origin as this page, so the address they are already browsing is the one the
// agent needs.
export const agentPrompt = (origin, userId) =>
    `Load instructions for using our deployment orchestration platform from ${origin}${INSTRUCTIONS_PATH}?user_id=${userId}`;

// Started | Status | Origin | actions.
const ROW_GRID = "grid grid-cols-[minmax(6rem,1fr)_minmax(8rem,1.4fr)_minmax(6rem,1fr)_auto] items-center gap-3";
const headerCellClass = "text-[10px] font-medium uppercase tracking-wide text-gray-500";
const iconButtonClass = "rounded p-1.5 text-gray-400 transition-colors hover:bg-gray-700 disabled:cursor-not-allowed disabled:opacity-25 disabled:hover:bg-transparent cursor-pointer";

function copyButton(text, label) {
    const copied = van.state(false);
    return button(
        {
            type: "button",
            class: "btn-secondary text-sm py-1.5 px-3 shrink-0 inline-flex items-center gap-1.5 cursor-pointer",
            title: () => copied.val ? "Copied" : label,
            "aria-label": () => copied.val ? "Copied" : label,
            onclick: async () => {
                try {
                    await navigator.clipboard.writeText(text());
                    copied.val = true;
                    setTimeout(() => copied.val = false, 2000);
                } catch {
                    copied.val = false;
                }
            },
        },
        () => copied.val ? checkIcon({class: "w-4 h-4 text-green-400"}) : copyIcon({class: "w-4 h-4"}),
        () => copied.val ? "Copied" : "Copy",
    );
}

export function agentSessions() {
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

    const pendingRow = (session) => div(
        {class: `${ROW_GRID} rounded-sm border border-amber-500/40 bg-amber-500/5 px-2 py-1`},
        span({class: "truncate text-xs text-gray-300"}, formatHistoryTime(session.createdAt)),
        div(
            {class: "flex min-w-0 items-center gap-2"},
            span({class: "shrink-0 text-xs text-amber-400"}, "Awaiting approval"),
            // The code is the whole point of this row: it is what tells the
            // operator this request is the one their agent made.
            span({class: "truncate rounded bg-gray-950 px-1.5 py-0.5 font-mono text-xs text-gray-100"},
                session.approvalCode || ""),
        ),
        span({class: "truncate font-mono text-xs text-gray-500"}, session.requestingAddress || "unknown"),
        div(
            {class: "flex items-center justify-end gap-2"},
            button({
                type: "button",
                class: "btn-primary text-xs py-1 px-2.5 shrink-0 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed",
                disabled: () => busyID.val === session.id,
                title: "Approve this request and let the agent collect its token",
                onclick: () => { void approve(session); },
            }, "Approve"),
            button({
                type: "button",
                class: `${iconButtonClass} hover:text-red-400`,
                disabled: () => busyID.val === session.id,
                title: "Reject this request",
                "aria-label": "Reject this request",
                onclick: () => { void stop(session); },
            }, xIcon({size: 14})),
        ),
    );

    const sessionRow = (session) => {
        const status = sessionStatus(session);
        const stoppable = sessionStoppable(session);
        return div(
            {class: `${ROW_GRID} rounded-sm px-2 py-1 hover:bg-gray-800/40`},
            span({class: "truncate text-xs text-gray-300"}, formatHistoryTime(session.createdAt)),
            span({class: `truncate text-xs ${status.tone}`},
                status.label === "Active"
                    ? `Active until ${formatHistoryTime(session.expiresAt)}`
                    : status.label),
            span({class: "truncate font-mono text-xs text-gray-500"}, session.requestingAddress || "unknown"),
            div(
                {class: "flex items-center justify-end"},
                // A cross, not a bin: this ends the session, it does not remove
                // the record of it having existed.
                button({
                    type: "button",
                    class: `${iconButtonClass} hover:text-red-400`,
                    disabled: () => !stoppable || busyID.val === session.id,
                    title: stoppable ? "Revoke this session" : `Session is already ${status.label.toLowerCase()}`,
                    "aria-label": stoppable ? "Revoke this session" : `Session is already ${status.label.toLowerCase()}`,
                    onclick: () => { void stop(session); },
                }, xIcon({size: 14})),
            ),
        );
    };

    const promptText = () => agentPrompt(window.location.origin, loginS.val?.userId || 0);

    return div(
        {class: "flex flex-col gap-3"},
        div(
            {class: "card", "data-testid": "agent-prompt"},
            div(
                {class: "flex items-start justify-between gap-3"},
                div(
                    h2({class: "font-semibold"}, "Agent prompt"),
                    p({class: "mt-1 text-xs text-gray-400"},
                        "Give this to an agent. It will fetch its own instructions, request a session, and print an approval code for you to match below."),
                ),
                copyButton(promptText, "Copy agent prompt"),
            ),
            code(
                {
                    class: "app-scroll-x mt-3 block overflow-x-auto whitespace-pre rounded bg-gray-950 p-3 text-xs text-gray-200",
                    "data-testid": "agent-prompt-text",
                },
                promptText,
            ),
        ),
        div(
            {class: "card", "data-testid": "agent-session-list"},
            h2({class: "font-semibold"}, "Your sessions"),
            () => error.val ? p({class: "mt-2 text-xs text-red-400", "data-testid": "agent-session-error"}, error.val) : "",
            div(
                {class: `${ROW_GRID} mt-3 px-2 pb-1`},
                span({class: headerCellClass}, "Started"),
                span({class: headerCellClass}, "Status"),
                span({class: headerCellClass}, "Origin"),
                span({class: "sr-only"}, "Actions"),
            ),
            // The empty state is a row, so the header sits the same distance
            // above it as it does above a real session.
            () => {
                const ordered = orderSessions(agentSessionsS.val);
                if (!ordered.length) {
                    return div({class: `${ROW_GRID} px-2 py-1`},
                        span({class: "col-span-4 flex h-8 items-center justify-center text-xs text-gray-500"},
                            "No sessions yet"));
                }
                return div(
                    {class: "flex flex-col gap-1"},
                    ...ordered.map((session) => sessionPending(session) ? pendingRow(session) : sessionRow(session)),
                );
            },
        ),
    );
}
