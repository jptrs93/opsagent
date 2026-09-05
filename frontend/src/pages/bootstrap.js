import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, registrationOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";

const { p, form, div, h1, label, input, a } = van.tags;

export function bootstrapPage() {
    const status = van.state('');
    const step = van.state('password'); // 'password' | 'register'
    loadAuthMethods();

    const usernameInput = input({
        type: "text",
        "data-testid": "bootstrap-username-input",
        required: true,
        class: "text-input",
        placeholder: "Your name",
        autocomplete: "username"
    });

    const passwordInput = input({
        type: "password",
        "data-testid": "bootstrap-password-input",
        required: true,
        class: "text-input",
        placeholder: "Master password",
        autocomplete: "off"
    });

    const submitButton = spinnerButton("Authenticate", null, "btn-primary w-full", 'submit');
    submitButton.dataset.testid = "bootstrap-authenticate-button";

    const handlePasswordSubmit = async (e) => {
        e.preventDefault();
        submitButton.isSubmitting.val = true;
        status.val = '';
        try {
            const response = await capi.postV1AuthMaster({password: passwordInput.value, username: usernameInput.value});
            setLoginFromResponse(response);
            passwordInput.value = '';
            usernameInput.value = '';
            step.val = 'register';
        } catch (e) {
            status.val = p({class: 'text-red-400 text-sm'}, `${e.message}`);
        } finally {
            submitButton.isSubmitting.val = false;
        }
    };

    // The master-password form is mounted immediately, so it can be built once.
    const masterForm = form(
        {class: "flex flex-col gap-4", onsubmit: handlePasswordSubmit},
        p({class: "text-sm text-gray-400"}, "Enter your name and the master password to create your account."),
        label({class: "text-sm font-medium"}, "Username"),
        usernameInput,
        label({class: "text-sm font-medium"}, "Master password"),
        passwordInput,
        submitButton,
    );

    // Step-two controls are built when that step mounts. Built up front they
    // would sit detached while the operator types the master password, and
    // VanJS drops bindings on DOM disconnected for more than about a second.
    const registerButton = () => {
        const button = spinnerButton("Register passkey", async () => {
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
                    status.val = div({class: 'flex flex-col gap-2'},
                        p({class: 'text-yellow-400 text-sm'}, 'A passkey already exists for this account.'),
                        a({class: 'text-sm text-blue-400 hover:text-blue-300 cursor-pointer', onclick: () => navigate("/login")}, 'Go to login'),
                    );
                    return;
                }
                if (e?.name === 'NotAllowedError') {
                    status.val = div({class: 'flex flex-col gap-2'},
                        p({class: 'text-red-400 text-sm'}, passkeyNotAllowedMessage('Passkey registration', e)),
                        a({class: 'text-sm text-blue-400 hover:text-blue-300 cursor-pointer', onclick: () => navigate("/login")}, 'Go to login'),
                    );
                    return;
                }
                status.val = p({class: 'text-red-400 text-sm'}, passkeyServerErrorMessage('Passkey registration', e));
            }
        }, "btn-primary w-full", 'button');
        button.dataset.testid = "bootstrap-register-passkey-button";
        return button;
    };

    const passkeySection = (passwordEnabled) => browserSupportsPasskeys()
        ? div({class: "flex flex-col gap-2"},
            passwordEnabled
                ? p({class: "text-sm text-gray-400"}, "Or register a passkey instead. Passkeys need HTTPS with a trusted certificate, or plain HTTP on localhost.")
                : p({class: "text-sm text-green-400"}, "Authenticated. Now register a passkey for future logins."),
            registerButton())
        : p({class: "text-red-400 text-sm"}, "This browser does not support passkeys.");

    const setPasswordForm = () => {
        const newPasswordInput = input({
            type: "password",
            "data-testid": "bootstrap-new-password-input",
            required: true,
            minlength: 8,
            class: "text-input",
            placeholder: "New password (at least 8 characters)",
            autocomplete: "new-password",
        });
        const confirmPasswordInput = input({
            type: "password",
            "data-testid": "bootstrap-confirm-password-input",
            required: true,
            class: "text-input",
            placeholder: "Confirm password",
            autocomplete: "new-password",
        });
        const setPasswordButton = spinnerButton("Set password", null, "btn-primary w-full", 'submit');
        setPasswordButton.dataset.testid = "bootstrap-set-password-button";
        return form(
            {
                class: "flex flex-col gap-3",
                onsubmit: async (e) => {
                    e.preventDefault();
                    if (setPasswordButton.isSubmitting.val) return;
                    status.val = '';
                    if (newPasswordInput.value !== confirmPasswordInput.value) {
                        status.val = p({class: 'text-red-400 text-sm'}, 'Passwords do not match.');
                        return;
                    }
                    setPasswordButton.isSubmitting.val = true;
                    try {
                        const response = await capi.postV1AuthPasswordSet({password: newPasswordInput.value});
                        newPasswordInput.value = '';
                        confirmPasswordInput.value = '';
                        setLoginFromResponse(response);
                        navigate("/");
                    } catch (err) {
                        status.val = p({class: 'text-red-400 text-sm'}, err?.message || 'Setting the password failed.');
                    } finally {
                        setPasswordButton.isSubmitting.val = false;
                    }
                },
            },
            p({class: "text-sm text-gray-400"}, "Choose a password for future logins."),
            newPasswordInput,
            confirmPasswordInput,
            setPasswordButton,
        );
    };

    const stepTwo = () => {
        const methods = authMethodsS.val;
        if (methods.status === 'loading') return div({class: "h-12"});
        const passwordEnabled = methods.status === 'ready' && methods.passwordLoginEnabled;
        return div(
            {class: "flex flex-col gap-4"},
            methods.status === 'error'
                ? div({class: "flex flex-col gap-1", "data-testid": "bootstrap-methods-error"},
                    p({class: "text-yellow-400 text-sm"}, `Could not load the available sign-in methods: ${methods.error}`),
                    a({class: "text-sm text-blue-400 hover:text-blue-300 cursor-pointer", onclick: () => loadAuthMethods()}, "Retry"))
                : '',
            passwordEnabled
                ? div({class: "flex flex-col gap-4"},
                    p({class: "text-sm text-green-400"}, "Authenticated."),
                    setPasswordForm(),
                    div({class: "flex items-center gap-3 text-xs text-gray-500"},
                        div({class: "h-px flex-1 bg-gray-700"}), "or", div({class: "h-px flex-1 bg-gray-700"})),
                    passkeySection(true))
                : passkeySection(false),
        );
    };

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] mx-auto my-auto"},
            h1({class: "text-2xl font-bold"}, "First time setup"),
            () => step.val === 'password' ? masterForm : stepTwo(),
            () => status.val || '',
        )
    );
}
