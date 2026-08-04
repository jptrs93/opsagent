import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {checkIcon, copyIcon} from "../lib/icons.js";
import {formatHistoryTime} from "../lib/date.js";

const {button, code, div, h2, p, span} = van.tags;

export const TOKEN_ENV_VAR = "OPENDEPLOY_TOKEN";

export const exportLine = (token) => `export ${TOKEN_ENV_VAR}=${token}`;

export function apiTokenGenerator() {
    const token = van.state("");
    const expiry = van.state(null);
    const error = van.state("");
    const copied = van.state(false);

    const generate = async () => {
        error.val = "";
        copied.val = false;
        try {
            const response = await capi.postV1AuthTokenGenerate();
            token.val = response?.token || "";
            expiry.val = response?.expiry || null;
            if (!token.val) error.val = "Server returned an empty token";
        } catch (e) {
            token.val = "";
            expiry.val = null;
            error.val = e.message || "Failed to generate token";
        }
    };

    const copy = async () => {
        if (!token.val) return;
        try {
            await navigator.clipboard.writeText(exportLine(token.val));
            copied.val = true;
            setTimeout(() => copied.val = false, 2000);
        } catch {
            error.val = "Copy failed - select the command and copy it manually";
        }
    };

    return div(
        {class: "card", "data-testid": "api-token-generator"},
        div(
            {class: "flex items-start justify-between gap-3"},
            div(
                h2({class: "font-semibold"}, "Command-line token"),
                p({class: "mt-1 text-xs text-gray-400"},
                    "Generates a bearer token valid for 12 hours, with the same access as your current session."),
            ),
            spinnerButton(
                () => token.val ? "Regenerate" : "Generate token",
                generate,
                "btn-primary text-sm py-1.5 px-3 shrink-0",
            ),
        ),
        () => error.val ? p({class: "mt-3 text-xs text-red-400", "data-testid": "api-token-error"}, error.val) : "",
        () => !token.val ? "" : div(
            {class: "mt-4 pt-4 border-t border-gray-700"},
            div(
                {class: "flex items-center justify-between gap-3 mb-2"},
                span({class: "text-xs text-gray-500"},
                    () => expiry.val ? `Expires ${formatHistoryTime(expiry.val)}` : "",
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
                    "data-testid": "api-token-export",
                },
                () => exportLine(token.val),
            ),
            p({class: "mt-2 text-xs text-gray-500"},
                "Store it somewhere safe - it is not recoverable after you leave this page."),
        ),
    );
}
