// Visual replica of today's src/pages/login.js, on the mock, for side-by-side
// comparison with the proposals. Markup and classes match the real page.
import van from "vanjs-core";
import {spinnerButton} from "/src/components/spinnerbutton.js";
import {caHelpBody, loginController} from "./shared.js";

const {a, details, div, form, input, label, p, summary} = van.tags;

export function currentDesign({methods, actions}) {
    const ctl = loginController(actions);

    const passkeySection = (available) => {
        if (!available) {
            return p({class: "text-gray-500 text-sm text-center"}, "Passkeys are unavailable on this server.");
        }
        if (!actions.browserSupportsPasskeys()) {
            return p({class: "text-red-400 text-sm text-center"}, "This browser does not support passkeys.");
        }
        return spinnerButton("Sign in with passkey", ctl.signInWithPasskey, "btn-primary w-full text-lg py-3", "button", () => ctl.busy.val);
    };

    const passwordForm = () => {
        const usernameInput = input({type: "text", required: true, class: "text-input", placeholder: "Your name", autocomplete: "username"});
        const passwordInput = input({type: "password", required: true, class: "text-input", placeholder: "Master password", autocomplete: "current-password"});
        const submit = spinnerButton("Sign in", null, "btn-primary w-full", "submit", () => ctl.busy.val);
        return form(
            {
                class: "flex flex-col gap-3",
                onsubmit: async (e) => {
                    e.preventDefault();
                    if (ctl.busy.val) return;
                    submit.isSubmitting.val = true;
                    try {
                        await ctl.signInWithPassword(usernameInput.value, passwordInput.value);
                    } finally {
                        submit.isSubmitting.val = false;
                    }
                },
            },
            label({class: "text-sm font-medium"}, "Username"),
            usernameInput,
            label({class: "text-sm font-medium"}, "Master password"),
            passwordInput,
            submit,
        );
    };

    const divider = () => div({class: "flex items-center gap-3 text-xs text-gray-500"},
        div({class: "h-px flex-1 bg-gray-700"}), "or", div({class: "h-px flex-1 bg-gray-700"}));

    const body = () => {
        if (methods.status === "loading") return div({class: "h-12"});
        if (methods.status === "error") {
            return div(
                {class: "flex flex-col gap-4"},
                div({class: "flex flex-col gap-1"},
                    p({class: "text-yellow-400 text-sm"}, `Could not load the available sign-in methods: ${methods.error}`),
                    a({class: "text-sm text-blue-400 hover:text-blue-300 cursor-pointer", onclick: actions.retryMethods}, "Retry")),
                passkeySection(true),
            );
        }
        if (!methods.passwordLoginEnabled) return passkeySection(methods.passkeyLoginEnabled);
        return div({class: "flex flex-col gap-4"}, passwordForm(), divider(), passkeySection(methods.passkeyLoginEnabled));
    };

    const caTrustHelp = () => details(
        {class: "min-w-0 rounded border border-gray-700 bg-gray-800/40 px-3 py-2 text-sm text-gray-300"},
        summary({class: "cursor-pointer select-none text-gray-400 hover:text-gray-200"}, "Browser warning about the certificate?"),
        div({class: "mt-2 flex max-h-[50vh] min-w-0 flex-col gap-2 overflow-y-auto"}, caHelpBody(actions)),
    );

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] max-w-[560px] max-h-dvh overflow-y-auto mx-auto my-auto"},
            body(),
            () => ctl.error.val ? p({class: "text-red-400 text-sm"}, ctl.error.val) : "",
            methods.localCaAvailable ? caTrustHelp() : "",
            div(
                {class: "text-center mt-2"},
                a({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer", href: actions.bootstrapHref, onclick: (e) => e.preventDefault()}, "First time setup"),
            ),
        ),
    );
}
