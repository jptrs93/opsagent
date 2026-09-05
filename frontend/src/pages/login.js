import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, loginOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";

const { p, div, a, form, input, label } = van.tags;

export function loginPage() {
    const loginErr = van.state('');
    // One attempt at a time across both methods, so a slow failing password
    // request can never land after a passkey login and clear its session.
    const busy = van.state(false);
    loadAuthMethods();

    // Controls are built inside the reactive branch that mounts them. VanJS
    // drops bindings on DOM that stays disconnected for about a second, so
    // anything constructed up front and mounted after discovery would lose
    // its spinner and disabled state.
    const passkeySection = () => {
        if (!browserSupportsPasskeys()) {
            return p({class: "text-red-400 text-sm text-center"}, "This browser does not support passkeys.");
        }
        const button = spinnerButton("Sign in with passkey", async () => {
            busy.val = true;
            loginErr.val = '';
            try {
                const startResponse = await capi.postV1AuthPasskeyLoginStart();
                const credential = await navigator.credentials.get(loginOptionsFromJSONBytes(startResponse.optionsJson));
                if (!credential) {
                    throw new Error('Passkey sign-in returned no credential.');
                }
                const response = await capi.postV1AuthPasskeyLoginFinish({
                    sessionId: startResponse.sessionId,
                    credentialJson: credentialToJSONBytes(credential),
                });
                setLoginFromResponse(response);
                navigate("/");
            } catch (e) {
                if (e?.name === 'NotAllowedError') {
                    loginErr.val = p({class: 'text-red-400 text-sm'}, passkeyNotAllowedMessage('Passkey sign-in', e));
                    return;
                }
                loginErr.val = p({class: 'text-red-400 text-sm'}, passkeyServerErrorMessage('Passkey sign-in', e));
            } finally {
                busy.val = false;
            }
        }, "btn-primary w-full text-lg py-3", 'button', () => busy.val);
        button.dataset.testid = "login-passkey-button";
        return button;
    };

    const passwordForm = () => {
        const usernameInput = input({
            type: "text",
            "data-testid": "login-username-input",
            required: true,
            class: "text-input",
            placeholder: "Username",
            autocomplete: "username",
        });
        const passwordInput = input({
            type: "password",
            "data-testid": "login-password-input",
            required: true,
            class: "text-input",
            placeholder: "Password",
            autocomplete: "current-password",
        });
        const submit = spinnerButton("Sign in", null, "btn-primary w-full", 'submit', () => busy.val);
        submit.dataset.testid = "login-password-button";
        return form(
            {
                class: "flex flex-col gap-3",
                onsubmit: async (e) => {
                    e.preventDefault();
                    if (busy.val) return;
                    busy.val = true;
                    submit.isSubmitting.val = true;
                    loginErr.val = '';
                    try {
                        const response = await capi.postV1AuthPasswordLogin({username: usernameInput.value, password: passwordInput.value});
                        passwordInput.value = '';
                        setLoginFromResponse(response);
                        navigate("/");
                    } catch (err) {
                        loginErr.val = p({class: 'text-red-400 text-sm'}, err?.message || 'Sign-in failed.');
                    } finally {
                        submit.isSubmitting.val = false;
                        busy.val = false;
                    }
                },
            },
            label({class: "text-sm font-medium"}, "Username"),
            usernameInput,
            label({class: "text-sm font-medium"}, "Password"),
            passwordInput,
            submit,
        );
    };

    const divider = () => div({class: "flex items-center gap-3 text-xs text-gray-500"},
        div({class: "h-px flex-1 bg-gray-700"}), "or", div({class: "h-px flex-1 bg-gray-700"}));

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] mx-auto my-auto"},
            () => {
                const methods = authMethodsS.val;
                if (methods.status === 'loading') return div({class: "h-12"});
                if (methods.status === 'error') {
                    return div(
                        {class: "flex flex-col gap-4"},
                        div({class: "flex flex-col gap-1", "data-testid": "login-methods-error"},
                            p({class: "text-yellow-400 text-sm"}, `Could not load the available sign-in methods: ${methods.error}`),
                            a({class: "text-sm text-blue-400 hover:text-blue-300 cursor-pointer", onclick: () => loadAuthMethods()}, "Retry")),
                        passkeySection(),
                    );
                }
                if (!methods.passwordLoginEnabled) return passkeySection();
                return div({class: "flex flex-col gap-4"}, passwordForm(), divider(), passkeySection());
            },
            () => loginErr.val || '',
            div(
                {class: "text-center mt-2"},
                a({
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: () => navigate("/bootstrap")
                }, "First time setup")
            )
        )
    );
}
