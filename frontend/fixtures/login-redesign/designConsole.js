// Design C — "Console": one compact card with a header band (wordmark, host
// chip), a grouped field box, and a footer band holding the secondary links.
// Certificate help expands inline under the footer so the card stays the
// single surface on the page.
import van from "vanjs-core";
import {alertIcon, busyButton, caHelpBody, fingerprintIcon, keyIcon, loginController, logoMark, submitForm, visibilityToggle} from "./shared.js";
import {chevronDownIcon} from "/src/lib/icons.js";

const {a, button, div, form, input, label, p, span} = van.tags;

const primaryClass = "flex w-full items-center justify-center rounded-md bg-brand px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-blue-500";
const outlineClass = "flex w-full items-center justify-center rounded-md border border-gray-600 bg-transparent px-4 py-2 text-sm text-gray-200 transition-colors hover:bg-gray-700/60";

export function consoleDesign({methods, actions}) {
    const ctl = loginController(actions);
    const caOpen = van.state(false);
    const ready = methods.status === "ready";
    // A local CA implies TLS; the fixture itself is served over plain HTTP.
    const secure = window.location.protocol === "https:" || methods.localCaAvailable;

    // Grouped fields: one bordered box, a hairline between rows, the label as
    // a micro caption inside each row. The whole box lights up on focus.
    const fieldRow = (text, inputEl, trailing) => label(
        {class: "flex cursor-text items-center gap-2 px-3 py-1.5"},
        div({class: "min-w-0 flex-1"},
            span({class: "block text-[10px] uppercase tracking-wide text-gray-500"}, text),
            inputEl),
        trailing || "",
    );
    const fieldInput = (attrs) => input({
        class: "w-full bg-transparent text-sm text-gray-100 placeholder-gray-600 focus:outline-none",
        ...attrs,
    });

    const passwordForm = () => {
        const usernameInput = fieldInput({type: "text", required: true, placeholder: "Your name", autocomplete: "username"});
        const passwordInput = fieldInput({type: "password", required: true, placeholder: "Master password", autocomplete: "current-password"});
        const submit = busyButton({label: "Sign in", type: "submit", class: primaryClass, disabledWhen: () => ctl.busy.val});
        const f = form(
            {class: "flex flex-col gap-3"},
            div({class: "divide-y divide-gray-700 rounded-md border border-gray-600 bg-gray-900/70 transition-colors focus-within:border-brand focus-within:ring-1 focus-within:ring-brand"},
                fieldRow("Username", usernameInput),
                fieldRow("Master password", passwordInput, visibilityToggle(passwordInput, "shrink-0"))),
            submit,
        );
        return submitForm(f, submit, usernameInput, passwordInput, ctl);
    };

    const passkeyBlock = (primary) => {
        if (!methods.passkeyLoginEnabled) {
            return p({class: "text-center text-xs text-gray-500"}, "Passkeys are not enabled on this server.");
        }
        if (!actions.browserSupportsPasskeys()) {
            return p({class: "rounded-md border border-gray-700 bg-gray-900/40 px-3 py-2 text-center text-xs text-gray-400"}, "This browser does not support passkeys.");
        }
        return div({class: "flex flex-col gap-2"},
            busyButton({
                label: "Sign in with passkey",
                icon: () => (primary ? fingerprintIcon("h-[18px] w-[18px]") : keyIcon("h-4 w-4 text-gray-400")),
                onClick: ctl.signInWithPasskey,
                class: primary ? `${primaryClass} py-2.5` : outlineClass,
                disabledWhen: () => ctl.busy.val,
            }),
            primary ? p({class: "text-center text-xs text-gray-500"}, "Your browser will prompt for a passkey registered on this server.") : "");
    };

    const orRow = () => div({class: "flex items-center gap-3 text-[10px] uppercase tracking-wide text-gray-500"},
        div({class: "h-px flex-1 bg-gray-700/80"}), "or", div({class: "h-px flex-1 bg-gray-700/80"}));

    const banner = (tone, ...children) => div({class: `flex items-start gap-2 rounded-md border px-3 py-2 text-xs ${tone}`},
        alertIcon("mt-px h-3.5 w-3.5 shrink-0"), span({class: "min-w-0"}, ...children));

    const methodsBody = () => {
        if (methods.status === "loading") {
            return div({class: "flex animate-pulse flex-col gap-3"},
                div({class: "h-[74px] rounded-md border border-gray-700 bg-gray-900/40"}), div({class: "h-9 rounded-md bg-gray-800/70"}));
        }
        if (methods.status === "error") {
            return div({class: "flex flex-col gap-4"},
                banner("border-amber-900/50 bg-amber-950/30 text-amber-200/90",
                    "Could not load the sign-in methods: ", span({class: "font-mono"}, methods.error), " ",
                    button({type: "button", class: "underline underline-offset-2 hover:text-amber-100 cursor-pointer", onclick: actions.retryMethods}, "Retry")),
                passkeyBlock(true));
        }
        if (!methods.passwordLoginEnabled) return passkeyBlock(true);
        return div({class: "flex flex-col gap-4"}, passwordForm(), orRow(), passkeyBlock(false));
    };

    const hostChip = div(
        {class: "flex items-center gap-1.5 rounded-full border border-gray-700 bg-gray-950/50 px-2 py-0.5 font-mono text-[11px] text-gray-400", title: secure ? "Served over TLS" : "Served over plain HTTP"},
        span({class: `h-1.5 w-1.5 rounded-full ${secure ? "bg-emerald-400" : "bg-amber-400"}`}),
        window.location.host,
    );

    const footerLink = "flex items-center gap-1 text-gray-400 transition-colors hover:text-gray-100 cursor-pointer";

    return div(
        {class: "flex min-h-dvh w-dvw items-center justify-center p-4"},
        div(
            {class: "w-full max-w-[420px] overflow-hidden rounded-lg border border-gray-700/80 bg-surface shadow-2xl shadow-black/60"},
            div({class: "h-0.5 bg-gradient-to-r from-brand via-sky-400 to-brand"}),
            div(
                {class: "flex items-center justify-between gap-3 border-b border-gray-700/80 bg-gray-900/50 px-4 py-2.5"},
                div({class: "flex items-center gap-2 text-sm"},
                    logoMark({size: 20}),
                    span({class: "font-semibold text-white"}, "OpenDeploy"),
                    span({class: "text-gray-600"}, "/"),
                    span({class: "text-gray-400"}, "Sign in")),
                hostChip,
            ),
            div(
                {class: "flex flex-col gap-4 p-5"},
                () => ctl.error.val ? banner("border-red-900/60 bg-red-950/40 text-red-300", ctl.error.val) : "",
                methodsBody(),
            ),
            div(
                {class: "flex items-center justify-between border-t border-gray-700/80 bg-gray-900/40 px-4 py-2 text-xs"},
                a({class: footerLink, href: actions.bootstrapHref, onclick: (e) => e.preventDefault()}, "First-time setup"),
                ready && methods.localCaAvailable
                    ? button({type: "button", class: footerLink, "aria-expanded": () => String(caOpen.val), onclick: () => { caOpen.val = !caOpen.val; }},
                        "Certificate help",
                        chevronDownIcon({class: () => `h-3.5 w-3.5 transition-transform ${caOpen.val ? "rotate-180" : ""}`}))
                    : "",
            ),
            () => caOpen.val
                ? div({class: "app-scroll max-h-[40vh] overflow-y-auto border-t border-gray-700/80 bg-gray-900/30 p-4"}, caHelpBody(actions))
                : "",
        ),
    );
}
