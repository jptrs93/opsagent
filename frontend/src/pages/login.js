import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {caTrustHelp} from "../components/caTrustHelp.js";
import {authButtonBase, authButtonClass, authCard, authLinkClass, divider, fieldGroup, fieldInput, fieldRow, footerBand, footerText, notice, revealToggle, transportRow, withIcon} from "../components/authCard.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, loginOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";
import {keyRoundIcon} from "../lib/icons.js";

const {a, button, div, form, p, span} = van.tags;

// The login page on the shared auth card: the username and password form
// first, the passkey button under it, or the passkey button alone when the
// cluster requires passkeys; and a footer band with the transport row and
// the enrol link.
export function loginPage() {
    const loginErr = van.state('');
    // One attempt at a time across both methods, so a slow failing password
    // request can never land after a passkey login and clear its session.
    const busy = van.state(false);
    const caOpen = van.state(false);
    loadAuthMethods();

    // Controls are built inside the reactive branch that mounts them. VanJS
    // drops bindings on DOM that stays disconnected for about a second, so
    // anything constructed up front and mounted after discovery would lose
    // its spinner and disabled state.
    const passkeySection = (available) => {
        if (!available) {
            return p({class: "text-center text-sm text-gray-500"}, "Passkeys are not enabled on this server.");
        }
        if (!browserSupportsPasskeys()) {
            return p({class: "rounded-lg border border-gray-800 bg-gray-900/40 px-3 py-2 text-center text-sm text-gray-400"}, "This browser does not support passkeys.");
        }
        const signIn = async () => {
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
                loginErr.val = e?.name === 'NotAllowedError'
                    ? passkeyNotAllowedMessage('Passkey sign-in', e)
                    : passkeyServerErrorMessage('Passkey sign-in', e);
            } finally {
                busy.val = false;
            }
        };
        const button = spinnerButton(withIcon(keyRoundIcon({class: "h-4 w-4"}), "Sign in with passkey"), signIn,
            authButtonClass, 'button', () => busy.val, {base: authButtonBase});
        button.dataset.testid = "login-passkey-button";
        return button;
    };

    const passwordForm = () => {
        const usernameInput = fieldInput({
            type: "text",
            "data-testid": "login-username-input",
            required: true,
            placeholder: "Username",
            autocomplete: "username",
        });
        const passwordInput = fieldInput({
            type: "password",
            "data-testid": "login-password-input",
            required: true,
            placeholder: "Password",
            autocomplete: "current-password",
        });
        const submit = spinnerButton("Sign in", null, authButtonClass, 'submit', () => busy.val, {base: authButtonBase});
        submit.dataset.testid = "login-password-button";
        return form(
            {
                class: "flex flex-col gap-4",
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
                        loginErr.val = err?.message || 'Sign-in failed.';
                    } finally {
                        submit.isSubmitting.val = false;
                        busy.val = false;
                    }
                },
            },
            fieldGroup(
                fieldRow("Username", usernameInput),
                fieldRow("Password", passwordInput, revealToggle(passwordInput))),
            submit,
        );
    };

    const methodsSection = () => {
        const methods = authMethodsS.val;
        if (methods.status === 'loading') {
            return div({class: "flex animate-pulse flex-col gap-4"},
                div({class: "h-[86px] rounded-lg border border-gray-700 bg-gray-900/40"}), div({class: "h-10 rounded-lg bg-gray-800/70"}), div({class: "h-10 rounded-lg bg-gray-800/70"}));
        }
        if (methods.status === 'error') {
            return div(
                {class: "flex flex-col gap-4"},
                notice("warning", "login-methods-error",
                    "Could not load the available sign-in methods: ", span({class: "font-mono text-xs"}, methods.error), " ",
                    button({type: "button", class: "cursor-pointer underline decoration-amber-200/40 underline-offset-2 hover:text-amber-100", onclick: () => loadAuthMethods()}, "Retry")),
                passwordForm(),
            );
        }
        if (methods.requirePasskey) {
            return div({class: "flex flex-col gap-3"},
                passkeySection(methods.passkeyLoginEnabled),
                p({class: "text-center text-xs text-gray-500", "data-testid": "login-passkey-required"}, "This cluster requires passkey sign-in."));
        }
        if (!methods.passkeyLoginEnabled) return passwordForm();
        return div({class: "flex flex-col gap-5"}, passwordForm(), divider("or"), passkeySection(true));
    };

    return authCard({
        title: "Sign in",
        body: [
            () => loginErr.val ? notice("error", "login-error", loginErr.val) : '',
            methodsSection,
        ],
        footer: () => footerBand(
            transportRow(authMethodsS.val, caOpen, "login-ca-open"),
            footerText("New here, or locked out? ", a({class: authLinkClass, "data-testid": "login-enrol-link", onclick: () => navigate("/enrol")}, "Enrol or recover a user")),
        ),
        overlays: [caTrustHelp(caOpen)],
    });
}
