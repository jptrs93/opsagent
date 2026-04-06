import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {browserSupportsPasskeys, credentialToJSONBytes, loginOptionsFromJSONBytes} from "../util/webauthn.js";

const { p, div, h1, a } = van.tags;

export function loginPage() {
    const loginErr = van.state('');
    const passkeySupported = browserSupportsPasskeys();

    const passkeyButton = spinnerButton("Sign in with passkey", async () => {
        loginErr.val = '';
        try {
            const startResponse = await capi.postV1AuthPasskeyLoginStart({});
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
                loginErr.val = p({class: 'text-red-400 text-sm'}, 'Passkey sign-in was cancelled.');
                return;
            }
            loginErr.val = p({class: 'text-red-400 text-sm'}, `${e.message}`);
        }
    }, "btn-primary w-full text-lg py-3", 'button');

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] mx-auto my-auto"},
            h1({class: "text-2xl font-bold text-center"}, "OpsAgent"),
            passkeySupported
                ? passkeyButton
                : p({class: "text-red-400 text-sm text-center"}, "This browser does not support passkeys."),
            loginErr,
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
