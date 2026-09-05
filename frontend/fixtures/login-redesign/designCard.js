// Design D — "Card": Console's shell around Centered's content. The card
// (header band, grouped field box, footer band) sits on the
// Centered backdrop; the header is the single "OpenDeploy Sign in" line, the
// passkey button comes first with the password form after it, both buttons
// in one brand-tinted style, and the footer band is the connection panel (transport with the Trust the CA
// action, enabled methods) plus the setup link. Certificate help is the
// overlay, never inline.
import van from "vanjs-core";
import {alertIcon, busyButton, caHelpModal, keyIcon, loginController, logoMark, shieldIcon, submitForm, visibilityToggle} from "./shared.js";

const {a, button, div, form, h1, input, label, main, p, span} = van.tags;

// One button style for both methods, between the filled primary and the
// outlined secondary: a brand tint with a brand border.
const buttonClass = "flex w-full items-center justify-center rounded-lg border border-brand/40 bg-brand/15 px-4 py-2.5 text-sm font-medium text-blue-100 transition-colors hover:border-brand/70 hover:bg-brand/25";

export function cardDesign({methods, actions}) {
    const ctl = loginController(actions);
    const caOpen = van.state(false);
    // A local CA implies TLS; the fixture itself is served over plain HTTP.
    const secure = window.location.protocol === "https:" || methods.localCaAvailable;

    // Grouped fields from Console: one bordered box, a hairline between rows,
    // the label as a micro caption inside each row, one focus ring for the box.
    const fieldRow = (text, inputEl, trailing) => label(
        {class: "flex cursor-text items-center gap-2 px-3 py-2"},
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
        const submit = busyButton({label: "Sign in", type: "submit", class: buttonClass, disabledWhen: () => ctl.busy.val});
        const f = form(
            {class: "flex flex-col gap-4"},
            div({class: "divide-y divide-gray-700 rounded-lg border border-gray-700 bg-gray-900/60 transition-colors focus-within:border-brand focus-within:ring-2 focus-within:ring-brand/25"},
                fieldRow("Username", usernameInput),
                fieldRow("Master password", passwordInput, visibilityToggle(passwordInput, "shrink-0"))),
            submit,
        );
        return submitForm(f, submit, usernameInput, passwordInput, ctl);
    };

    const passkeyBlock = () => {
        if (!methods.passkeyLoginEnabled) {
            return p({class: "text-center text-sm text-gray-500"}, "Passkeys are not enabled on this server.");
        }
        if (!actions.browserSupportsPasskeys()) {
            return p({class: "rounded-lg border border-gray-800 bg-gray-900/40 px-3 py-2 text-center text-sm text-gray-400"}, "This browser does not support passkeys.");
        }
        return busyButton({
            label: "Sign in with passkey",
            icon: () => keyIcon("h-4 w-4"),
            onClick: ctl.signInWithPasskey,
            class: buttonClass,
            disabledWhen: () => ctl.busy.val,
        });
    };

    // Console's divider: small-caps "or" on slightly stronger hairlines.
    const divider = () => div({class: "flex items-center gap-3 text-[10px] uppercase tracking-wide text-gray-500"},
        div({class: "h-px flex-1 bg-gray-700/80"}), "or", div({class: "h-px flex-1 bg-gray-700/80"}));

    const notice = (tone, ...children) => div({class: `flex items-start gap-2 rounded-lg border px-3 py-2.5 text-sm ${tone}`},
        alertIcon("mt-0.5 h-4 w-4 shrink-0"), span({class: "min-w-0"}, ...children));

    const methodsBody = () => {
        if (methods.status === "loading") {
            return div({class: "flex animate-pulse flex-col gap-4"},
                div({class: "h-[86px] rounded-lg border border-gray-700 bg-gray-900/40"}), div({class: "h-10 rounded-lg bg-gray-800/70"}));
        }
        if (methods.status === "error") {
            return div({class: "flex flex-col gap-4"},
                notice("border-amber-900/50 bg-amber-950/30 text-amber-200/90",
                    "Could not load the sign-in methods: ", span({class: "font-mono text-xs"}, methods.error), " ",
                    button({type: "button", class: "cursor-pointer underline decoration-amber-200/40 underline-offset-2 hover:text-amber-100", onclick: actions.retryMethods}, "Retry")),
                passkeyBlock());
        }
        if (!methods.passwordLoginEnabled) return passkeyBlock();
        // Passkey leads even when password login is on; the password form
        // follows, its submit in the same style.
        return div({class: "flex flex-col gap-5"}, passkeyBlock(), divider(), passwordForm());
    };

    // Footer band: the transport row with the certificate action, then the
// setup link.
    const row = (term, value, trailing) => div(
        {class: "flex items-center gap-3 px-4 py-2"},
        span({class: "w-20 shrink-0 text-[11px] uppercase tracking-wide text-gray-500"}, term),
        span({class: "min-w-0 flex-1 truncate text-xs text-gray-300"}, value),
        trailing || "",
    );
    const dot = (color) => span({class: `h-1.5 w-1.5 shrink-0 rounded-full ${color}`});
    const transport = secure
        ? (methods.localCaAvailable
            ? span({class: "flex items-center gap-1.5"}, dot("bg-amber-400"), "HTTPS · local CA")
            : span({class: "flex items-center gap-1.5"}, dot("bg-emerald-400"), "HTTPS"))
        : span({class: "flex items-center gap-1.5"}, dot("bg-gray-500"), "Plain HTTP");

    const footer = div(
        {class: "divide-y divide-gray-800 border-t border-gray-700/80 bg-gray-900/40"},
        row("Transport", transport, methods.localCaAvailable
            ? button({type: "button", class: "flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 transition-colors hover:border-gray-600 hover:bg-gray-800 hover:text-white", onclick: () => { caOpen.val = true; }},
                shieldIcon("h-3 w-3"), "Trust the CA")
            : ""),
        div({class: "px-4 py-2 text-xs text-gray-500"}, "First time here? ",
            a({class: "cursor-pointer text-brand hover:text-blue-400", href: actions.bootstrapHref, onclick: (e) => e.preventDefault()}, "Set up your passkey")),
    );

    return div(
        {class: "relative min-h-dvh w-dvw overflow-hidden bg-gray-950"},
        div({class: "pointer-events-none absolute inset-0 opacity-30",
            style: "background-image: radial-gradient(#334155 1px, transparent 1px); background-size: 22px 22px; mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%); -webkit-mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%);"}),
        div({class: "pointer-events-none absolute left-1/2 top-[-14rem] h-[28rem] w-[40rem] -translate-x-1/2 rounded-full bg-brand/15 blur-3xl"}),
        main(
            {class: "relative flex min-h-dvh items-center justify-center p-4 sm:p-10"},
            div(
                {class: "w-full max-w-[420px] overflow-hidden rounded-xl border border-gray-700/80 bg-surface shadow-2xl shadow-black/60"},
                div(
                    {class: "border-b border-gray-700/80 px-5 py-3.5"},
                    h1({class: "flex items-center gap-2.5 text-lg font-semibold tracking-tight text-white"},
                        logoMark({size: 24}), "OpenDeploy", span({class: "font-normal text-gray-500"}, "Sign in")),
                ),
                div(
                    {class: "flex flex-col gap-5 p-5"},
                    () => ctl.error.val ? notice("border-red-900/60 bg-red-950/40 text-red-300", ctl.error.val) : "",
                    methodsBody(),
                ),
                footer,
            ),
        ),
        caHelpModal(caOpen, actions),
    );
}
