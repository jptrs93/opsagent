import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {browserSupportsPasskeys, credentialToJSONBytes, registrationOptionsFromJSONBytes} from "../util/webauthn.js";

const { p, form, div, h1, h2, label, input, a } = van.tags;

export function bootstrapPage() {
    const status = van.state('');
    const step = van.state('password'); // 'password' | 'register'

    const usernameInput = input({
        type: "text",
        required: true,
        class: "text-input",
        placeholder: "Your name",
        autocomplete: "username"
    });

    const passwordInput = input({
        type: "password",
        required: true,
        class: "text-input",
        placeholder: "Master password",
        autocomplete: "off"
    });

    const submitButton = spinnerButton("Authenticate", null, "btn-primary w-full", 'submit');

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

    const registerButton = spinnerButton("Register passkey", async () => {
        status.val = '';
        try {
            const startResponse = await capi.postV1AuthPasskeyRegisterStart({});
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
                    p({class: 'text-red-400 text-sm'}, 'Registration was cancelled. If a passkey already exists, try logging in instead.'),
                    a({class: 'text-sm text-blue-400 hover:text-blue-300 cursor-pointer', onclick: () => navigate("/login")}, 'Go to login'),
                );
                return;
            }
            status.val = p({class: 'text-red-400 text-sm'}, `${e.message}`);
        }
    }, "btn-primary w-full", 'button');

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] mx-auto my-auto"},
            h1({class: "text-2xl font-bold"}, "First time setup"),
            () => {
                if (step.val === 'password') {
                    return form(
                        {class: "flex flex-col gap-4", onsubmit: handlePasswordSubmit},
                        p({class: "text-sm text-gray-400"}, "Enter your name and the master password to create your passkey."),
                        label({class: "text-sm font-medium"}, "Username"),
                        usernameInput,
                        label({class: "text-sm font-medium"}, "Master password"),
                        passwordInput,
                        submitButton,
                        status.val
                    );
                }
                return div(
                    {class: "flex flex-col gap-4"},
                    p({class: "text-sm text-green-400"}, "Authenticated. Now register a passkey for future logins."),
                    browserSupportsPasskeys()
                        ? registerButton
                        : p({class: "text-red-400 text-sm"}, "This browser does not support passkeys."),
                    status.val
                );
            }
        )
    );
}
