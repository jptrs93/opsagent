// Design B — "Minimal": no card, no panel. A narrow centred column with an
// outline mark, a single heading, underline inputs and one high-contrast
// button. Everything secondary (setup, certificate help, host) sits in a
// quiet footer row; the certificate help opens as an overlay.
import van from "vanjs-core";
import {busyButton, caHelpModal, fingerprintIcon, keyIcon, loginController, logoMark, submitForm, visibilityToggle} from "./shared.js";

const {a, button, div, form, h1, input, label, p, span} = van.tags;

const fieldClass = "w-full border-0 border-b border-gray-700 bg-transparent px-0 py-2 text-[15px] text-gray-100 placeholder-gray-600 transition-colors focus:border-gray-200 focus:outline-none focus:ring-0";
const labelClass = "block text-[11px] uppercase tracking-[0.14em] text-gray-500";
const primaryClass = "flex w-full items-center justify-center rounded-md bg-gray-100 px-4 py-2.5 text-sm font-medium text-gray-900 transition-colors hover:bg-white";
const ghostClass = "flex w-full items-center justify-center rounded-md border border-gray-800 px-4 py-2.5 text-sm text-gray-300 transition-colors hover:border-gray-600 hover:text-gray-100";
const darkSpinner = "border-gray-900/25 border-t-gray-900";

export function minimalDesign({methods, actions}) {
    const ctl = loginController(actions);
    const caOpen = van.state(false);
    const ready = methods.status === "ready";

    const passkeyBlock = (primary) => {
        if (!methods.passkeyLoginEnabled) {
            return p({class: "text-center text-sm text-gray-600"}, "Passkeys are not enabled on this server.");
        }
        if (!actions.browserSupportsPasskeys()) {
            return p({class: "text-center text-sm text-gray-500"}, "This browser does not support passkeys.");
        }
        return div({class: "flex flex-col gap-3"},
            busyButton({
                label: primary ? "Continue with passkey" : "Use a passkey instead",
                icon: () => (primary ? fingerprintIcon("h-[18px] w-[18px]") : keyIcon("h-4 w-4 text-gray-500")),
                onClick: ctl.signInWithPasskey,
                class: primary ? primaryClass : ghostClass,
                spinnerClass: primary ? darkSpinner : "border-white/30 border-t-white",
                disabledWhen: () => ctl.busy.val,
            }),
            primary ? p({class: "text-center text-xs text-gray-600"}, "Your browser will prompt for a passkey registered on this server.") : "");
    };

    const passwordForm = () => {
        const usernameInput = input({type: "text", required: true, class: fieldClass, placeholder: "Your name", autocomplete: "username"});
        const passwordInput = input({type: "password", required: true, class: `${fieldClass} pr-8`, placeholder: "••••••••••••", autocomplete: "current-password"});
        const submit = busyButton({label: "Sign in", type: "submit", class: `${primaryClass} mt-2`, spinnerClass: darkSpinner, disabledWhen: () => ctl.busy.val});
        const f = form(
            {class: "flex flex-col gap-5"},
            div(label({class: labelClass}, "Username"), usernameInput),
            div(label({class: labelClass}, "Master password"),
                div({class: "relative"}, passwordInput, visibilityToggle(passwordInput, "absolute inset-y-0 right-0 flex items-center"))),
            submit,
        );
        return submitForm(f, submit, usernameInput, passwordInput, ctl);
    };

    const orRow = () => div({class: "flex items-center gap-3 text-[11px] uppercase tracking-[0.14em] text-gray-600"},
        div({class: "h-px flex-1 bg-gray-800"}), "or", div({class: "h-px flex-1 bg-gray-800"}));

    const methodsBody = () => {
        if (methods.status === "loading") {
            return div({class: "flex animate-pulse flex-col gap-6"},
                div({class: "h-9 border-b border-gray-800"}), div({class: "h-9 border-b border-gray-800"}), div({class: "h-10 rounded-md bg-gray-800/60"}));
        }
        if (methods.status === "error") {
            return div({class: "flex flex-col gap-5"},
                p({class: "text-sm text-amber-300/90"}, "Could not load the sign-in methods. ",
                    button({type: "button", class: "underline underline-offset-2 hover:text-amber-100 cursor-pointer", onclick: actions.retryMethods}, "Retry")),
                passkeyBlock(true));
        }
        if (!methods.passwordLoginEnabled) return passkeyBlock(true);
        return div({class: "flex flex-col gap-6"}, passwordForm(), orRow(), passkeyBlock(false));
    };

    const footerLink = (text, onclick, href) => a({
        class: "text-gray-500 transition-colors hover:text-gray-200 cursor-pointer",
        href, onclick: (e) => { e.preventDefault(); onclick?.(); },
    }, text);
    const dot = () => span({class: "text-gray-800"}, "·");

    return div(
        {class: "fixed inset-0 overflow-y-auto bg-gray-950"},
        div(
            {class: "flex min-h-full flex-col items-center justify-center px-6 py-12"},
            div(
                {class: "w-full max-w-[340px]"},
                div({class: "flex justify-center text-gray-200"}, logoMark({size: 44, outline: true})),
                h1({class: "mt-6 text-center text-2xl font-semibold tracking-tight text-white"}, "Sign in to OpenDeploy"),
                p({class: "mt-1.5 text-center font-mono text-xs text-gray-600"}, window.location.host),
                div({class: "mt-10"}, methodsBody()),
                () => ctl.error.val ? p({class: "mt-5 text-sm leading-relaxed text-red-400"}, ctl.error.val) : "",
                div(
                    {class: "mt-12 flex items-center justify-center gap-3 text-xs"},
                    footerLink("First-time setup", null, actions.bootstrapHref),
                    methods.localCaAvailable ? dot() : "",
                    methods.localCaAvailable ? footerLink("Certificate help", () => { caOpen.val = true; }) : "",
                    ready ? dot() : "",
                    ready ? span({class: "text-gray-600"}, methods.passwordLoginEnabled ? "Password login on" : "Passkeys only") : "",
                ),
            ),
        ),
        caHelpModal(caOpen, actions),
    );
}
