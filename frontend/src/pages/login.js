import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {caTrustHelp} from "../components/caTrustHelp.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, loginOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";
import {alertCircleIcon, eyeOffIcon, eyeOpenIcon, fingerprintIcon, keyRoundIcon, logoMark, shieldCheckIcon} from "../lib/icons.js";

const {a, button, div, form, h1, input, label, main, p, span} = van.tags;

const fieldClass = "w-full rounded-lg border border-gray-700 bg-gray-900/60 px-3.5 py-2.5 text-sm text-gray-100 placeholder-gray-500 transition-colors focus:border-brand focus:outline-none focus:ring-2 focus:ring-brand/25";
const labelClass = "mb-1.5 block text-sm font-medium text-gray-300";
// Button shapes are passed as spinnerButton's base so its default padding
// and radius do not fight these.
const primaryBase = "flex w-full items-center justify-center rounded-lg px-4 py-2.5 text-sm font-medium";
const primaryClass = "bg-brand text-white shadow-lg shadow-brand/20 hover:bg-blue-500";
const secondaryClass = "border border-gray-700 bg-gray-800/60 text-gray-200 hover:border-gray-600 hover:bg-gray-800";

// The login page: one centred column with a single header line, the sign-in
// controls, a connection panel (transport, enabled methods) and the setup
// link. Password login leads when it is enabled, since that setting exists
// for installs where the browser will not run WebAuthn; otherwise the
// passkey button is the one primary action.
export function loginPage() {
    const loginErr = van.state('');
    // One attempt at a time across both methods, so a slow failing password
    // request can never land after a passkey login and clear its session.
    const busy = van.state(false);
    const caOpen = van.state(false);
    loadAuthMethods();

    const withIcon = (icon, text) => span({class: "inline-flex items-center gap-2"}, icon, text);

    // Controls are built inside the reactive branch that mounts them. VanJS
    // drops bindings on DOM that stays disconnected for about a second, so
    // anything constructed up front and mounted after discovery would lose
    // its spinner and disabled state.
    const passkeySection = (available, primary) => {
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
        const icon = primary ? fingerprintIcon({class: "h-5 w-5"}) : keyRoundIcon({class: "h-4 w-4"});
        const button = spinnerButton(withIcon(icon, "Sign in with passkey"), signIn,
            primary ? `${primaryClass} py-3 text-base` : secondaryClass, 'button', () => busy.val, {base: primaryBase});
        button.dataset.testid = "login-passkey-button";
        return div({class: "flex flex-col gap-3"},
            button,
            primary ? p({class: "text-center text-xs text-gray-500"}, "Your browser will prompt for a passkey registered on this server.") : '');
    };

    // Master-password login: the name is created on first use, so this is
    // both the login and the account-creation path when it is enabled.
    const passwordForm = () => {
        const usernameInput = input({
            type: "text",
            "data-testid": "login-username-input",
            required: true,
            class: fieldClass,
            placeholder: "Your name",
            autocomplete: "username",
        });
        const passwordInput = input({
            type: "password",
            "data-testid": "login-password-input",
            required: true,
            class: `${fieldClass} pr-10`,
            placeholder: "Master password",
            autocomplete: "current-password",
        });
        const shown = van.state(false);
        const reveal = button({
            type: "button",
            tabindex: -1,
            title: () => shown.val ? "Hide password" : "Show password",
            class: "absolute inset-y-0 right-0 flex cursor-pointer items-center px-3 text-gray-500 transition-colors hover:text-gray-200",
            onclick: () => { shown.val = !shown.val; passwordInput.type = shown.val ? "text" : "password"; },
        }, () => shown.val ? eyeOffIcon({class: "h-4 w-4"}) : eyeOpenIcon({class: "h-4 w-4"}));
        const submit = spinnerButton("Sign in", null, primaryClass, 'submit', () => busy.val, {base: primaryBase});
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
            div(label({class: labelClass}, "Username"), usernameInput),
            div(label({class: labelClass}, "Master password"), div({class: "relative"}, passwordInput, reveal)),
            submit,
        );
    };

    const divider = () => div({class: "flex items-center gap-3 text-xs text-gray-500"},
        div({class: "h-px flex-1 bg-gray-800"}), "or", div({class: "h-px flex-1 bg-gray-800"}));

    const notice = (tone, testid, ...children) => div(
        {class: `flex items-start gap-2 rounded-lg border px-3 py-2.5 text-sm ${tone}`, "data-testid": testid},
        alertCircleIcon({class: "mt-0.5 h-4 w-4 shrink-0"}), span({class: "min-w-0"}, ...children));

    const methodsSection = () => {
        const methods = authMethodsS.val;
        if (methods.status === 'loading') {
            return div({class: "flex animate-pulse flex-col gap-4"},
                div({class: "h-10 rounded-lg bg-gray-800/70"}), div({class: "h-10 rounded-lg bg-gray-800/70"}), div({class: "h-10 rounded-lg bg-gray-800/40"}));
        }
        if (methods.status === 'error') {
            return div(
                {class: "flex flex-col gap-4"},
                notice("border-amber-900/50 bg-amber-950/30 text-amber-200/90", "login-methods-error",
                    "Could not load the available sign-in methods: ", span({class: "font-mono text-xs"}, methods.error), " ",
                    button({type: "button", class: "cursor-pointer underline decoration-amber-200/40 underline-offset-2 hover:text-amber-100", onclick: () => loadAuthMethods()}, "Retry")),
                passkeySection(true, true),
            );
        }
        if (!methods.passwordLoginEnabled) return passkeySection(methods.passkeyLoginEnabled, true);
        return div({class: "flex flex-col gap-5"}, passwordForm(), divider(), passkeySection(methods.passkeyLoginEnabled, false));
    };

    // Connection panel: what the browser is talking to. The certificate
    // action sits on the transport row it belongs to.
    const row = (term, value, trailing) => div(
        {class: "flex items-center gap-3 px-3 py-2"},
        span({class: "w-20 shrink-0 text-[11px] uppercase tracking-wide text-gray-500"}, term),
        span({class: "min-w-0 flex-1 truncate text-xs text-gray-300"}, value),
        trailing || '',
    );
    const dot = (color) => span({class: `h-1.5 w-1.5 shrink-0 rounded-full ${color}`});
    const connectionPanel = () => {
        const methods = authMethodsS.val;
        const secure = window.location.protocol === "https:";
        const transport = secure
            ? (methods.localCaAvailable
                ? span({class: "flex items-center gap-1.5"}, dot("bg-amber-400"), "HTTPS · local CA")
                : span({class: "flex items-center gap-1.5"}, dot("bg-emerald-400"), "HTTPS"))
            : span({class: "flex items-center gap-1.5"}, dot("bg-gray-500"), "Plain HTTP");
        const methodList = methods.status !== 'ready' ? "…" : [
            methods.passkeyLoginEnabled ? "Passkey" : null,
            methods.passwordLoginEnabled ? "Master password" : null,
        ].filter(Boolean).join(" · ") || "None";
        return div(
            {class: "divide-y divide-gray-800 rounded-lg border border-gray-800 bg-gray-900/40"},
            row("Transport", transport, methods.localCaAvailable
                ? button({
                    type: "button",
                    class: "flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 transition-colors hover:border-gray-600 hover:bg-gray-800 hover:text-white",
                    "data-testid": "login-ca-open",
                    onclick: () => { caOpen.val = true; },
                }, shieldCheckIcon({class: "h-3 w-3"}), "Trust the CA")
                : ''),
            row("Sign-in", methodList),
        );
    };

    return div(
        {class: "app-scroll relative h-dvh w-dvw overflow-y-auto bg-gray-950"},
        // Backdrop: a faint dot grid fading out from the top, under a soft
        // brand glow. Both sit at the top of the page and scroll with it.
        div({class: "pointer-events-none absolute inset-x-0 top-0 h-dvh opacity-30",
            style: "background-image: radial-gradient(#334155 1px, transparent 1px); background-size: 22px 22px; mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%); -webkit-mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%);"}),
        div({class: "pointer-events-none absolute left-1/2 top-[-14rem] h-[28rem] w-[40rem] -translate-x-1/2 rounded-full bg-brand/15 blur-3xl"}),
        main(
            {class: "relative flex min-h-full items-center justify-center p-6 sm:p-10"},
            div(
                {class: "w-full max-w-sm"},
                h1({class: "flex items-center gap-2.5 text-xl font-semibold tracking-tight text-white"},
                    logoMark({size: 28}), "OpenDeploy", span({class: "font-normal text-gray-500"}, "Sign in")),
                div(
                    {class: "mt-8 flex flex-col gap-5"},
                    () => loginErr.val ? notice("border-red-900/60 bg-red-950/40 text-red-300", "login-error", loginErr.val) : '',
                    methodsSection,
                ),
                div({class: "mt-8"}, connectionPanel),
                p({class: "mt-6 text-sm text-gray-500"}, "First time here? ",
                    a({class: "cursor-pointer text-brand hover:text-blue-400", onclick: () => navigate("/bootstrap")}, "Set up your passkey")),
            ),
        ),
        caTrustHelp(caOpen),
    );
}
