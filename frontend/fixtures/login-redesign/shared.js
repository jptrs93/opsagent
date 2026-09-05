// Pieces every design shares: the sign-in controller (the behaviour of
// src/pages/login.js with the API calls swapped for the mock), a button with
// a configurable spinner, the logo mark, icons the app does not have yet, and
// the CA trust help content.
import van from "vanjs-core";
import {passkeyNotAllowedMessage} from "/src/util/webauthn.js";
import {eyeOpenIcon, eyeOffIcon, closeIcon} from "/src/lib/icons.js";

const {a, button, div, p, pre, span} = van.tags;
const {svg, path, rect, circle} = van.tags("http://www.w3.org/2000/svg");

// --- controller -------------------------------------------------------------

// One attempt at a time across both methods, as in the real page, so a slow
// failing password request can never land after a passkey login.
export function loginController(actions) {
    const busy = van.state(false);
    const error = van.state("");
    const run = async (fn) => {
        if (busy.val) return;
        busy.val = true;
        error.val = "";
        try {
            await fn();
        } catch (e) {
            error.val = e?.name === "NotAllowedError"
                ? passkeyNotAllowedMessage("Passkey sign-in", e)
                : (e?.message || "Sign-in failed.");
        } finally {
            busy.val = false;
        }
    };
    return {
        busy,
        error,
        signInWithPasskey: () => run(actions.passkeyLogin),
        signInWithPassword: (username, password) => run(() => actions.passwordLogin(username, password)),
    };
}

// submitForm wires a form's submit to the controller and mirrors the busy
// state onto the submit button's spinner.
export function submitForm(formEl, submitButton, usernameInput, passwordInput, ctl) {
    formEl.onsubmit = async (e) => {
        e.preventDefault();
        if (ctl.busy.val) return;
        submitButton.spinning.val = true;
        try {
            await ctl.signInWithPassword(usernameInput.value, passwordInput.value);
            if (!ctl.error.val) passwordInput.value = "";
        } finally {
            submitButton.spinning.val = false;
        }
    };
    return formEl;
}

// --- buttons ----------------------------------------------------------------

// busyButton is spinnerButton with the spinner colour exposed, so a light
// button (design B) can show a dark spinner. Adopting that design means adding
// an options.spinnerClass to src/components/spinnerbutton.js.
export function busyButton({label, icon, onClick, type = "button", class: cls, spinnerClass = "border-white/30 border-t-white", disabledWhen = () => false, testid}) {
    const spinning = van.state(false);
    const disabled = () => spinning.val || disabledWhen();
    const b = button(
        {
            type,
            class: () => `${cls} relative ${disabled() ? "cursor-not-allowed opacity-70" : "cursor-pointer"}`,
            disabled,
            "data-testid": testid,
            onclick: async (e) => {
                if (!onClick || disabled()) return;
                spinning.val = true;
                try { await onClick(e); } finally { spinning.val = false; }
            },
        },
        span({class: () => `inline-flex items-center justify-center gap-2 ${spinning.val ? "invisible" : ""}`}, icon ? icon() : "", label),
        span({class: () => `absolute inset-0 flex items-center justify-center ${spinning.val ? "" : "hidden"}`},
            span({class: `h-[1.2em] w-[1.2em] animate-spin rounded-full border-[0.15em] ${spinnerClass}`})),
    );
    b.spinning = spinning;
    return b;
}

// --- visibility toggle -------------------------------------------------------

export function visibilityToggle(input, cls = "") {
    const shown = van.state(false);
    return button({
        type: "button",
        tabindex: -1,
        title: () => shown.val ? "Hide password" : "Show password",
        class: `cursor-pointer text-gray-500 transition-colors hover:text-gray-200 ${cls}`,
        onclick: () => { shown.val = !shown.val; input.type = shown.val ? "text" : "password"; },
    }, () => shown.val ? eyeOffIcon({class: "h-4 w-4"}) : eyeOpenIcon({class: "h-4 w-4"}));
}

// --- marks and icons ---------------------------------------------------------

// logoMark: a rounded tile with a deploy glyph. `outline` draws it as a stroke
// on transparent for the sparse design.
export function logoMark({size = 32, outline = false, class: cls = ""} = {}) {
    return svg(
        {viewBox: "0 0 32 32", width: size, height: size, class: cls, fill: "none"},
        rect({x: 1.5, y: 1.5, width: 29, height: 29, rx: 7.5, ...(outline
            ? {stroke: "currentColor", "stroke-width": 1.5}
            : {fill: "var(--color-brand)"})}),
        path({d: "M16 8.5l7 11.5H9z", fill: outline ? "currentColor" : "#fff", opacity: outline ? 1 : 0.95}),
        path({d: "M11.5 23.5h9", stroke: outline ? "currentColor" : "#fff", "stroke-width": 2, "stroke-linecap": "round", opacity: 0.55}),
    );
}

const icon = (cls, ...children) => svg({
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", "stroke-width": "2",
    "stroke-linecap": "round", "stroke-linejoin": "round", class: cls,
}, ...children);

export const fingerprintIcon = (cls = "h-5 w-5") => icon(cls,
    path({d: "M12 10a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4"}),
    path({d: "M14 13.12c0 2.38 0 6.38-1 8.88"}),
    path({d: "M17.29 21.02c.12-.6.43-2.3.5-3.02"}),
    path({d: "M2 12a10 10 0 0 1 18-6"}),
    path({d: "M2 16h.01"}),
    path({d: "M21.8 16c.2-2 .131-5.354 0-6"}),
    path({d: "M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2"}),
    path({d: "M8.65 22c.21-.66.45-1.32.57-2"}),
    path({d: "M9 6.8a6 6 0 0 1 9 5.2v2"}),
);

export const keyIcon = (cls = "h-4 w-4") => icon(cls,
    path({d: "M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"}),
    circle({cx: "16.5", cy: "7.5", r: ".5", fill: "currentColor"}),
);

export const shieldIcon = (cls = "h-4 w-4") => icon(cls,
    path({d: "M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"}),
    path({d: "m9 12 2 2 4-4"}),
);

export const alertIcon = (cls = "h-4 w-4") => icon(cls,
    circle({cx: "12", cy: "12", r: "10"}),
    path({d: "M12 8v4"}),
    path({d: "M12 16h.01"}),
);

// --- CA trust help -----------------------------------------------------------

const PLATFORMS = [
    {key: "macos", label: "macOS", steps: [
        ["Trust the CA", "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt"],
    ]},
    {key: "linux", label: "Linux", steps: [
        ["System store", "sudo cp opendeploy-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates"],
        ["Chrome / Chromium also", "certutil -d sql:$HOME/.pki/nssdb -A -t \"C,,\" -n \"OpenDeploy Local CA\" -i opendeploy-ca.crt"],
    ]},
    {key: "windows", label: "Windows", steps: [
        ["Trust the CA", "certutil -addstore -f ROOT opendeploy-ca.crt"],
    ]},
    {key: "firefox", label: "Firefox", steps: [
        ["Use the OS store: set this to true in about:config", "security.enterprise_roots.enabled"],
        ["Or import the file under", "Settings › Privacy & Security › Certificates › View Certificates › Import"],
    ]},
];

// caHelpBody is the instruction content: download/copy row, platform tabs,
// commands with per-block copy buttons, and the caution at the end. Designs
// differ only in what they wrap it in (details, modal, card footer).
export function caHelpBody(actions) {
    const tab = van.state("macos");
    const copied = van.state(false);
    const copyErr = van.state("");

    const copyText = async (text, done) => {
        copyErr.val = "";
        try {
            await navigator.clipboard.writeText(text);
            done.val = true;
            setTimeout(() => { done.val = false; }, 1500);
        } catch (e) {
            copyErr.val = `Copy failed: ${e.message}`;
        }
    };

    const chip = "rounded-md border border-gray-600 px-2.5 py-1 text-xs text-gray-200 transition-colors hover:bg-gray-700 cursor-pointer no-underline";

    const tabButton = ({key, label}) => button({
        type: "button",
        role: "tab",
        "aria-selected": () => String(tab.val === key),
        class: () => `-mb-px cursor-pointer border-b-2 px-2 py-1 text-xs transition-colors ${tab.val === key
            ? "border-brand text-gray-100"
            : "border-transparent text-gray-400 hover:text-gray-200"}`,
        onclick: () => { tab.val = key; },
    }, label);

    const cmd = (text) => {
        const done = van.state(false);
        return div(
            {class: "flex items-start gap-1"},
            pre({class: "min-w-0 flex-1 whitespace-pre-wrap break-all rounded bg-code px-2 py-1 text-[11px] text-gray-300 select-all"}, text),
            button({
                type: "button",
                class: "shrink-0 rounded border border-gray-600 px-2 py-1 text-[11px] text-gray-300 hover:bg-gray-700 cursor-pointer",
                title: "Copy command",
                onclick: () => copyText(text, done),
            }, () => done.val ? "Copied" : "Copy"),
        );
    };

    return div(
        {class: "flex min-w-0 flex-col gap-3"},
        p({class: "text-xs text-gray-400"}, "This server has its own certificate authority. Trust it once, restart the browser, then reload."),
        div(
            {class: "flex flex-wrap items-center gap-2"},
            a({class: chip, href: actions.caURL, download: "opendeploy-ca.crt"}, "Download opendeploy-ca.crt"),
            button({type: "button", class: chip, onclick: async () => {
                copyErr.val = "";
                try {
                    await navigator.clipboard.writeText(await actions.fetchCA());
                    copied.val = true;
                    setTimeout(() => { copied.val = false; }, 1500);
                } catch (e) {
                    copyErr.val = `Copy failed: ${e.message}`;
                }
            }}, () => copied.val ? "Copied" : "Copy PEM"),
            () => copyErr.val ? span({class: "text-xs text-red-400"}, copyErr.val) : "",
        ),
        div({class: "flex gap-1 border-b border-gray-700", role: "tablist"}, ...PLATFORMS.map(tabButton)),
        () => {
            const active = PLATFORMS.find((x) => x.key === tab.val);
            return div({class: "flex flex-col gap-2"},
                ...active.steps.map(([title, text]) => div(p({class: "mb-1 text-xs text-gray-400"}, title), cmd(text))));
        },
        p({class: "flex items-start gap-2 rounded-md border border-amber-900/50 bg-amber-950/30 px-2.5 py-2 text-xs text-amber-200/90"},
            alertIcon("mt-0.5 h-3.5 w-3.5 shrink-0"),
            span("A trusted CA can sign certificates for any site. Only do this for a server you control, from a machine you trust, and remove the CA when you no longer need it.")),
    );
}

// caHelpModal renders the help as a centred overlay while `open` is true.
// Closes on the backdrop, the close button, and Escape.
export function caHelpModal(open, actions) {
    const onKey = (e) => { if (e.key === "Escape") open.val = false; };
    return () => {
        if (!open.val) {
            window.removeEventListener("keydown", onKey);
            return "";
        }
        window.addEventListener("keydown", onKey);
        return div(
            {class: "fixed inset-0 z-40 flex items-center justify-center bg-black/60 p-4 backdrop-blur-[2px]",
                onclick: (e) => { if (e.target === e.currentTarget) open.val = false; }},
            div(
                {class: "flex max-h-[85vh] w-[540px] max-w-full flex-col overflow-hidden rounded-lg border border-gray-700 bg-surface shadow-2xl shadow-black/60"},
                div(
                    {class: "flex items-center justify-between border-b border-gray-700/80 px-4 py-3"},
                    div({class: "flex items-center gap-2 text-sm font-medium text-gray-100"}, shieldIcon("h-4 w-4 text-brand"), "Trust this server's certificate"),
                    button({type: "button", class: "rounded p-1 text-gray-400 hover:bg-gray-700 hover:text-gray-100 cursor-pointer", onclick: () => { open.val = false; }},
                        closeIcon({class: "h-4 w-4"})),
                ),
                div({class: "app-scroll min-h-0 overflow-y-auto p-4"}, caHelpBody(actions)),
            ),
        );
    };
}
