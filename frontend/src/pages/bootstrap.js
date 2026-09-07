import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {caTrustHelp} from "../components/caTrustHelp.js";
import {authButtonBase, authButtonClass, authCard, authLinkClass, fieldGroup, fieldInput, fieldRow, footerBand, footerText, notice, revealToggle, transportRow, withIcon} from "../components/authCard.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, registrationOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";
import {checkIcon, keyRoundIcon} from "../lib/icons.js";

const {a, div, form, p, span} = van.tags;

// First-time setup on the shared auth card: a two-step flow (master password,
// then passkey registration) with a step strip above it, and a footer band
// with the transport row and the way back to login.
export function bootstrapPage() {
    const status = van.state('');
    const step = van.state('password'); // 'password' | 'register'
    const caOpen = van.state(false);
    loadAuthMethods();

    const loginLink = (text = "Back to login") => a({class: authLinkClass, onclick: () => navigate("/login")}, text);

    // The step strip: the current step carries the brand tint, a finished
    // step a check, the next one stays muted.
    const stepStrip = () => {
        const current = step.val;
        const marker = (n, state) => span({
            class: `flex h-5 w-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-medium ${
                state === 'current' ? "border-brand/70 bg-brand/15 text-blue-100"
                    : state === 'done' ? "border-emerald-700/70 bg-emerald-950/40 text-emerald-300"
                        : "border-gray-700 text-gray-500"}`,
        }, state === 'done' ? checkIcon({class: "h-3 w-3"}) : String(n));
        const item = (n, text, state) => div({class: `flex items-center gap-2 text-xs ${state === 'current' ? "text-gray-100" : "text-gray-500"}`}, marker(n, state), text);
        return div(
            {class: "flex items-center gap-3"},
            item(1, "Master password", current === 'password' ? 'current' : 'done'),
            div({class: "h-px flex-1 bg-gray-700/80"}),
            item(2, "Passkey", current === 'register' ? 'current' : 'next'),
        );
    };

    // Step one: the master password mints a short-lived token that only
    // allows passkey registration. Built when the step mounts, like step two,
    // so its spinner binding is never left on detached DOM.
    const passwordStep = () => {
        const usernameInput = fieldInput({
            type: "text",
            "data-testid": "bootstrap-username-input",
            required: true,
            placeholder: "Your name",
            autocomplete: "username",
        });
        const passwordInput = fieldInput({
            type: "password",
            "data-testid": "bootstrap-password-input",
            required: true,
            placeholder: "Master password",
            autocomplete: "off",
        });
        const submit = spinnerButton("Authenticate", null, authButtonClass, 'submit', null, {base: authButtonBase});
        submit.dataset.testid = "bootstrap-authenticate-button";
        return form(
            {
                class: "flex flex-col gap-4",
                onsubmit: async (e) => {
                    e.preventDefault();
                    submit.isSubmitting.val = true;
                    status.val = '';
                    try {
                        const response = await capi.postV1AuthMaster({password: passwordInput.value, username: usernameInput.value});
                        setLoginFromResponse(response);
                        passwordInput.value = '';
                        usernameInput.value = '';
                        step.val = 'register';
                    } catch (err) {
                        status.val = notice("error", "bootstrap-error", err?.message || 'Authentication failed.');
                    } finally {
                        submit.isSubmitting.val = false;
                    }
                },
            },
            p({class: "text-sm text-gray-400"}, "Enter your name and the master password to create your passkey."),
            fieldGroup(
                fieldRow("Username", usernameInput),
                fieldRow("Master password", passwordInput, revealToggle(passwordInput))),
            submit,
        );
    };

    // Step two: register the passkey against the bootstrap token. Built when
    // the step mounts; built up front it would sit detached while the
    // operator types the master password, and VanJS drops bindings on DOM
    // disconnected for more than about a second.
    const registerButton = () => {
        const button = spinnerButton(withIcon(keyRoundIcon({class: "h-4 w-4"}), "Register passkey"), async () => {
            status.val = '';
            try {
                const startResponse = await capi.postV1AuthPasskeyRegisterStart();
                const credential = await navigator.credentials.create(
                    registrationOptionsFromJSONBytes(startResponse.optionsJson)
                );
                if (!credential) {
                    throw new Error('Passkey registration returned no credential.');
                }
                const response = await capi.postV1AuthPasskeyRegisterFinish({
                    sessionId: startResponse.sessionId,
                    credentialJson: credentialToJSONBytes(credential),
                });
                setLoginFromResponse(response);
                navigate("/");
            } catch (e) {
                if (e?.name === 'InvalidStateError') {
                    status.val = notice("warning", "bootstrap-error",
                        "A passkey already exists for this account. ", loginLink("Go to login"), ".");
                    return;
                }
                if (e?.name === 'NotAllowedError') {
                    status.val = notice("error", "bootstrap-error",
                        passkeyNotAllowedMessage('Passkey registration', e), " ", loginLink("Go to login"), ".");
                    return;
                }
                status.val = notice("error", "bootstrap-error", passkeyServerErrorMessage('Passkey registration', e));
            }
        }, authButtonClass, 'button', null, {base: authButtonBase});
        button.dataset.testid = "bootstrap-register-passkey-button";
        return button;
    };

    const registerStep = () => {
        const methods = authMethodsS.val;
        return div(
            {class: "flex flex-col gap-4"},
            browserSupportsPasskeys()
                ? notice("success", "bootstrap-authenticated", "Authenticated. Now register a passkey for future logins.")
                : notice("error", "bootstrap-authenticated", "Authenticated, but this browser does not support passkeys."),
            browserSupportsPasskeys() ? registerButton() : '',
            methods.status === 'ready' && methods.passwordLoginEnabled
                ? p({class: "text-xs text-gray-500"},
                    "Password login is enabled on this server, so you can also skip the passkey and ",
                    loginLink("sign in with your name and the master password"),
                    ".")
                : '',
        );
    };

    return authCard({
        title: "First time setup",
        body: [
            stepStrip,
            () => status.val || '',
            () => step.val === 'password' ? passwordStep() : registerStep(),
        ],
        footer: () => footerBand(
            transportRow(authMethodsS.val, caOpen, "bootstrap-ca-open"),
            footerText("Already have a passkey? ", a({class: authLinkClass, "data-testid": "bootstrap-back-to-login", onclick: () => navigate("/login")}, "Back to login")),
        ),
        overlays: [caTrustHelp(caOpen)],
    });
}
