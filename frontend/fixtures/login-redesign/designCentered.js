// Design A — "Centered": one column, no card, no side panel. A single header
// line above a labelled form, then a compact connection panel (transport,
// sign-in methods) with the certificate action right on the transport row. Certificate help opens
// as an overlay.
import van from "vanjs-core";
import {alertIcon, busyButton, caHelpModal, fingerprintIcon, keyIcon, loginController, logoMark, shieldIcon, submitForm, visibilityToggle} from "./shared.js";

const {a, button, div, form, h1, input, label, main, p, span} = van.tags;

const fieldClass = "w-full rounded-lg border border-gray-700 bg-gray-900/60 px-3.5 py-2.5 text-sm text-gray-100 placeholder-gray-500 transition-colors focus:border-brand focus:outline-none focus:ring-2 focus:ring-brand/25";
const labelClass = "mb-1.5 block text-sm font-medium text-gray-300";
const primaryClass = "flex w-full items-center justify-center rounded-lg bg-brand px-4 py-2.5 text-sm font-medium text-white shadow-lg shadow-brand/20 transition-colors hover:bg-blue-500";
const secondaryClass = "flex w-full items-center justify-center rounded-lg border border-gray-700 bg-gray-800/60 px-4 py-2.5 text-sm font-medium text-gray-200 transition-colors hover:border-gray-600 hover:bg-gray-800";

export function centeredDesign({methods, actions}) {
    const ctl = loginController(actions);
    const caOpen = van.state(false);
    const ready = methods.status === "ready";
    // A local CA implies TLS; the fixture itself is served over plain HTTP.
    const secure = window.location.protocol === "https:" || methods.localCaAvailable;

    const passkeyButton = (primary) => busyButton({
        label: "Continue with passkey",
        icon: () => (primary ? fingerprintIcon("h-5 w-5") : keyIcon("h-4 w-4")),
        onClick: ctl.signInWithPasskey,
        class: primary ? `${primaryClass} py-3 text-base` : secondaryClass,
        disabledWhen: () => ctl.busy.val,
    });

    const passkeyBlock = (primary) => {
        if (!methods.passkeyLoginEnabled) {
            return p({class: "text-center text-sm text-gray-500"}, "Passkeys are not enabled on this server.");
        }
        if (!actions.browserSupportsPasskeys()) {
            return p({class: "rounded-lg border border-gray-800 bg-gray-900/40 px-3 py-2 text-center text-sm text-gray-400"},
                "This browser does not support passkeys.");
        }
        return div({class: "flex flex-col gap-3"},
            passkeyButton(primary),
            primary ? p({class: "text-center text-xs text-gray-500"}, "Your browser will prompt for a passkey registered on this server.") : "");
    };

    const passwordForm = () => {
        const usernameInput = input({type: "text", required: true, class: fieldClass, placeholder: "Your name", autocomplete: "username"});
        const passwordInput = input({type: "password", required: true, class: `${fieldClass} pr-10`, placeholder: "Master password", autocomplete: "current-password"});
        const submit = busyButton({label: "Sign in", type: "submit", class: primaryClass, disabledWhen: () => ctl.busy.val});
        const f = form(
            {class: "flex flex-col gap-4"},
            div(label({class: labelClass}, "Username"), usernameInput),
            div(
                label({class: labelClass}, "Master password"),
                div({class: "relative"}, passwordInput, visibilityToggle(passwordInput, "absolute inset-y-0 right-0 flex items-center px-3")),
            ),
            submit,
        );
        return submitForm(f, submit, usernameInput, passwordInput, ctl);
    };

    const divider = (text) => div({class: "flex items-center gap-3 text-xs text-gray-500"},
        div({class: "h-px flex-1 bg-gray-800"}), text, div({class: "h-px flex-1 bg-gray-800"}));

    const errorBox = () => ctl.error.val
        ? div({class: "flex items-start gap-2 rounded-lg border border-red-900/60 bg-red-950/40 px-3 py-2.5 text-sm text-red-300"},
            alertIcon("mt-0.5 h-4 w-4 shrink-0"), span(ctl.error.val))
        : "";

    const methodsBody = () => {
        if (methods.status === "loading") {
            return div({class: "flex animate-pulse flex-col gap-4"},
                div({class: "h-10 rounded-lg bg-gray-800/70"}), div({class: "h-10 rounded-lg bg-gray-800/70"}), div({class: "h-10 rounded-lg bg-gray-800/40"}));
        }
        if (methods.status === "error") {
            return div({class: "flex flex-col gap-4"},
                div({class: "flex items-start gap-2 rounded-lg border border-amber-900/50 bg-amber-950/30 px-3 py-2.5 text-sm text-amber-200/90"},
                    alertIcon("mt-0.5 h-4 w-4 shrink-0"),
                    span("Could not load the sign-in methods: ", span({class: "font-mono text-xs"}, methods.error), " ",
                        button({type: "button", class: "underline decoration-amber-200/40 underline-offset-2 hover:text-amber-100 cursor-pointer", onclick: actions.retryMethods}, "Retry"))),
                passkeyBlock(true));
        }
        if (!methods.passwordLoginEnabled) return passkeyBlock(true);
        return div({class: "flex flex-col gap-5"}, passwordForm(), divider("or"), passkeyBlock(false));
    };

    // Connection panel: the server facts, one per row, with the certificate
    // action sitting on the transport row it belongs to.
    const row = (term, value, trailing) => div(
        {class: "flex items-center gap-3 px-3 py-2"},
        span({class: "w-20 shrink-0 text-[11px] uppercase tracking-wide text-gray-500"}, term),
        span({class: "min-w-0 flex-1 truncate text-xs text-gray-300"}, value),
        trailing || "",
    );
    const transport = secure
        ? (methods.localCaAvailable
            ? span({class: "flex items-center gap-1.5"}, span({class: "h-1.5 w-1.5 rounded-full bg-amber-400"}), "HTTPS · local CA")
            : span({class: "flex items-center gap-1.5"}, span({class: "h-1.5 w-1.5 rounded-full bg-emerald-400"}), "HTTPS"))
        : span({class: "flex items-center gap-1.5"}, span({class: "h-1.5 w-1.5 rounded-full bg-gray-500"}), "Plain HTTP");
    const methodList = !ready ? "…" : [
        methods.passkeyLoginEnabled ? "Passkey" : null,
        methods.passwordLoginEnabled ? "Master password" : null,
    ].filter(Boolean).join(" · ") || "None";

    const connectionPanel = div(
        {class: "divide-y divide-gray-800 rounded-lg border border-gray-800 bg-gray-900/40"},
        row("Transport", transport, methods.localCaAvailable
            ? button({type: "button", class: "flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 transition-colors hover:border-gray-600 hover:bg-gray-800 hover:text-white", onclick: () => { caOpen.val = true; }},
                shieldIcon("h-3 w-3"), "Trust the CA")
            : ""),
        row("Sign-in", methodList),
    );

    return div(
        {class: "relative min-h-dvh w-dvw overflow-hidden bg-gray-950"},
        div({class: "pointer-events-none absolute inset-0 opacity-30",
            style: "background-image: radial-gradient(#334155 1px, transparent 1px); background-size: 22px 22px; mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%); -webkit-mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%);"}),
        div({class: "pointer-events-none absolute left-1/2 top-[-14rem] h-[28rem] w-[40rem] -translate-x-1/2 rounded-full bg-brand/15 blur-3xl"}),
        main(
            {class: "relative flex min-h-dvh items-center justify-center p-6 sm:p-10"},
            div(
                {class: "w-full max-w-sm"},
                // One line of header: the mark, the product, and what this
                // page is. No greeting, no sentence about the methods; the
                // buttons say what they do.
                h1({class: "flex items-center gap-2.5 text-xl font-semibold tracking-tight text-white"},
                    logoMark({size: 28}), "OpenDeploy", span({class: "font-normal text-gray-500"}, "Sign in")),
                div({class: "mt-8 flex flex-col gap-5"}, errorBox, methodsBody()),
                div({class: "mt-8"}, connectionPanel),
                p({class: "mt-6 text-sm text-gray-500"}, "First time here? ",
                    a({class: "text-brand hover:text-blue-400 cursor-pointer", href: actions.bootstrapHref, onclick: (e) => e.preventDefault()}, "Set up your passkey")),
            ),
        ),
        caHelpModal(caOpen, actions),
    );
}
