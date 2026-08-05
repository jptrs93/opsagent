import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {checkIcon, copyIcon, xIcon} from "../lib/icons.js";
import {formatHistoryTime} from "../lib/date.js";
import {orderSessions, sessionExpired, sessionLive, sessionStatus} from "../lib/agentSessions.js";

const {button, code, div, h2, p, span} = van.tags;

export const TOKEN_ENV_VAR = "OPENDEPLOY_TOKEN";
export const URL_ENV_VAR = "OPENDEPLOY_URL";

// The API is served from the same origin as this page, so the address the
// operator is already browsing is the one their shell needs.
export const exportLines = (token, url) =>
    `export ${URL_ENV_VAR}=${url}\nexport ${TOKEN_ENV_VAR}=${token}`;

// Created | Expires | Token | actions.
const ROW_GRID = "grid grid-cols-[minmax(6rem,1fr)_minmax(6rem,1fr)_minmax(5rem,1fr)_auto] items-center gap-3";
const headerCellClass = "text-[10px] font-medium uppercase tracking-wide text-gray-500";

export function agentSessions() {
    const token = van.state("");
    const created = van.state(null);
    const sessions = van.state([]);
    const error = van.state("");
    const listError = van.state("");
    const copied = van.state(false);
    const loading = van.state(true);

    const refresh = async () => {
        try {
            const response = await capi.postV1AgentSessionsList({});
            sessions.val = response?.items || [];
            listError.val = "";
        } catch (e) {
            listError.val = e.message || "Failed to load sessions";
        } finally {
            loading.val = false;
        }
    };
    void refresh();

    const start = async () => {
        error.val = "";
        copied.val = false;
        try {
            const response = await capi.postV1AgentSessionsCreate();
            token.val = response?.token || "";
            created.val = response?.session || null;
            if (!token.val) error.val = "Server returned an empty token";
        } catch (e) {
            token.val = "";
            created.val = null;
            error.val = e.message || "Failed to start session";
        }
        await refresh();
    };

    const revoke = async (session) => {
        try {
            await capi.postV1AgentSessionsRevoke({id: session.id});
            // The token just shown is dead now, so stop offering it for copy.
            if (created.val?.id === session.id) {
                token.val = "";
                created.val = null;
            }
            listError.val = "";
        } catch (e) {
            listError.val = e.message || "Failed to revoke session";
        }
        await refresh();
    };

    const copy = async () => {
        if (!token.val) return;
        try {
            await navigator.clipboard.writeText(exportLines(token.val, window.location.origin));
            copied.val = true;
            setTimeout(() => copied.val = false, 2000);
        } catch {
            error.val = "Copy failed - select the command and copy it manually";
        }
    };

    const rowEl = (session) => {
        const status = sessionStatus(session);
        const isCurrent = created.val?.id === session.id;
        const revocable = sessionLive(session);
        return div(
            {class: `${ROW_GRID} rounded-sm px-2 py-1 ${isCurrent ? "bg-brand/10" : "hover:bg-gray-800/40"}`},
            div(
                {class: "flex min-w-0 items-center gap-2"},
                span({class: "truncate text-xs text-gray-300"}, formatHistoryTime(session.createdAt)),
                isCurrent ? span({class: "shrink-0 rounded bg-brand/20 px-1.5 py-0.5 text-[10px] text-blue-300"}, "This session") : "",
            ),
            span({class: `truncate text-xs ${status.tone}`},
                session.revoked || sessionExpired(session)
                    ? status.label
                    : `${status.label} until ${formatHistoryTime(session.expiresAt)}`),
            span({class: "truncate font-mono text-xs text-gray-500"}, `${session.tokenPrefix || ""}…`),
            div(
                {class: "flex items-center justify-end"},
                // A cross, not a bin: revoking ends this session, it does not
                // remove the row or touch the user.
                button({
                    type: "button",
                    class: "rounded p-1.5 text-gray-400 transition-colors hover:bg-gray-700 hover:text-red-400 disabled:cursor-not-allowed disabled:opacity-25 disabled:hover:bg-transparent cursor-pointer",
                    disabled: !revocable,
                    title: revocable ? "Revoke this session" : `Session is already ${status.label.toLowerCase()}`,
                    "aria-label": revocable ? "Revoke this session" : `Session is already ${status.label.toLowerCase()}`,
                    onclick: () => { void revoke(session); },
                }, xIcon({size: 14})),
            ),
        );
    };

    return div(
        {class: "flex flex-col gap-3"},
        div(
            {class: "card", "data-testid": "agent-session-starter"},
            div(
                {class: "flex items-start justify-between gap-3"},
                div(
                    h2({class: "font-semibold"}, "Agent sessions"),
                    p({class: "mt-1 text-xs text-gray-400"},
                        "Starts a session with a bearer token valid for 12 hours. It carries your access except for secret values, which it can list but not reveal or change."),
                ),
                spinnerButton("Start new session", start, "btn-primary text-sm py-1.5 px-3 shrink-0"),
            ),
            () => error.val ? p({class: "mt-3 text-xs text-red-400", "data-testid": "agent-session-error"}, error.val) : "",
            () => !token.val ? "" : div(
                {class: "mt-4 pt-4 border-t border-gray-700"},
                div(
                    {class: "flex items-center justify-between gap-3 mb-2"},
                    span({class: "text-xs text-gray-500"},
                        () => created.val ? `Expires ${formatHistoryTime(created.val.expiresAt)}` : "",
                    ),
                    button(
                        {
                            type: "button",
                            class: "btn-secondary text-sm py-1.5 px-3 shrink-0 inline-flex items-center gap-1.5 cursor-pointer",
                            title: () => copied.val ? "Copied" : "Copy export command",
                            "aria-label": () => copied.val ? "Copied" : "Copy export command",
                            onclick: copy,
                        },
                        () => copied.val ? checkIcon({class: "w-4 h-4 text-green-400"}) : copyIcon({class: "w-4 h-4"}),
                        () => copied.val ? "Copied" : "Copy",
                    ),
                ),
                code(
                    {
                        class: "app-scroll-x block overflow-x-auto whitespace-pre rounded bg-gray-950 p-3 text-xs text-gray-200",
                        "data-testid": "agent-session-export",
                    },
                    () => exportLines(token.val, window.location.origin),
                ),
                p({class: "mt-2 text-xs text-gray-500"},
                    "Store it somewhere safe - it is not recoverable after you leave this page."),
            ),
        ),
        div(
            {class: "card", "data-testid": "agent-session-list"},
            h2({class: "font-semibold"}, "Your sessions"),
            () => listError.val ? p({class: "mt-2 text-xs text-red-400"}, listError.val) : "",
            div(
                {class: `${ROW_GRID} mt-3 px-2 pb-1`},
                span({class: headerCellClass}, "Started"),
                span({class: headerCellClass}, "Status"),
                span({class: headerCellClass}, "Token"),
                span({class: "sr-only"}, "Actions"),
            ),
            // The empty state is a row, so the header sits the same distance
            // above it as it does above a real session.
            () => {
                const ordered = orderSessions(sessions.val);
                if (loading.val) {
                    return div({class: `${ROW_GRID} px-2 py-1`},
                        span({class: "col-span-4 flex h-8 items-center justify-center text-xs text-gray-500"}, "Loading sessions..."));
                }
                if (!ordered.length) {
                    return div({class: `${ROW_GRID} px-2 py-1`},
                        span({class: "col-span-4 flex h-8 items-center justify-center text-xs text-gray-500"}, "No sessions started yet"));
                }
                return div({class: "flex flex-col"}, ...ordered.map(rowEl));
            },
        ),
    );
}
