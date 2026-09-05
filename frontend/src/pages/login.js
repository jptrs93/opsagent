import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {capi} from "../capi/index.js";
import {setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {browserSupportsPasskeys, credentialToJSONBytes, loginOptionsFromJSONBytes, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";

const { p, div, a, form, input, label, details, summary, pre, button, span } = van.tags;

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
    const passkeySection = (available) => {
        if (!available) {
            return p({class: "text-gray-500 text-sm text-center"}, "Passkeys are unavailable on this server.");
        }
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

    // Master-password login: the name is created on first use, so this is
    // both the login and the account-creation path when it is enabled.
    const passwordForm = () => {
        const usernameInput = input({
            type: "text",
            "data-testid": "login-username-input",
            required: true,
            class: "text-input",
            placeholder: "Your name",
            autocomplete: "username",
        });
        const passwordInput = input({
            type: "password",
            "data-testid": "login-password-input",
            required: true,
            class: "text-input",
            placeholder: "Master password",
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
            label({class: "text-sm font-medium"}, "Master password"),
            passwordInput,
            submit,
        );
    };

    const divider = () => div({class: "flex items-center gap-3 text-xs text-gray-500"},
        div({class: "h-px flex-1 bg-gray-700"}), "or", div({class: "h-px flex-1 bg-gray-700"}));

    return div(
        {class: "min-h-dvh w-dvw flex"},
        div(
            {class: "card flex flex-col gap-4 p-8 min-w-[min(420px,90%)] max-w-[560px] max-h-dvh overflow-y-auto mx-auto my-auto"},
            () => {
                const methods = authMethodsS.val;
                if (methods.status === 'loading') return div({class: "h-12"});
                if (methods.status === 'error') {
                    return div(
                        {class: "flex flex-col gap-4"},
                        div({class: "flex flex-col gap-1", "data-testid": "login-methods-error"},
                            p({class: "text-yellow-400 text-sm"}, `Could not load the available sign-in methods: ${methods.error}`),
                            a({class: "text-sm text-blue-400 hover:text-blue-300 cursor-pointer", onclick: () => loadAuthMethods()}, "Retry")),
                        passkeySection(true),
                    );
                }
                if (!methods.passwordLoginEnabled) return passkeySection(methods.passkeyLoginEnabled);
                return div({class: "flex flex-col gap-4"}, passwordForm(), divider(), passkeySection(methods.passkeyLoginEnabled));
            },
            () => loginErr.val || '',
            () => authMethodsS.val.localCaAvailable ? caTrustHelp() : '',
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

// caTrustHelp is the in-page copy of the instructions the installer prints:
// how to make the browser trust the locally generated Web UI CA. It is shown
// whenever the server is serving under that CA, collapsed, so someone who
// continued through the browser warning can find it without leaving the page.
// The CA PEM is fetched once and inlined into each command as a heredoc, so
// trusting the CA is a single paste with no file to move around; until the
// PEM has arrived, or if it cannot be fetched, the file-based commands show.
function caTrustHelp() {
    const caURL = `${window.location.origin}/v1/tls/ca.crt`;
    const tab = van.state('macos');
    const copied = van.state(false);
    const copyErr = van.state('');
    const pemS = van.state('');

    const fetchPEM = async () => {
        const res = await fetch(caURL);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const text = (await res.text()).trim();
        if (!text.startsWith('-----BEGIN CERTIFICATE-----')) throw new Error('not a PEM certificate');
        pemS.val = text;
        return text;
    };
    fetchPEM().catch(() => {});

    const copyCA = async () => {
        copyErr.val = '';
        try {
            await navigator.clipboard.writeText(pemS.val || await fetchPEM());
            copied.val = true;
            setTimeout(() => { copied.val = false; }, 1500);
        } catch (e) {
            copyErr.val = `Copy failed: ${e.message}`;
        }
    };

    // Shown in place of the full PEM inside a displayed command block.
    const pemPlaceholder = '-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----';
    const heredoc = (pem) => `<<'EOF'\n${pem}\nEOF`;
    // A step is [title, buildCommand(pem)] where pem is '' when unavailable.
    const platforms = [
        {key: 'macos', label: 'macOS', steps: [
            ['Write the CA and trust it', (pem) => pem
                ? `cat > opendeploy-ca.crt ${heredoc(pem)}\nsudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt`
                : 'sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt'],
        ]},
        {key: 'linux', label: 'Linux', steps: [
            ['System store', (pem) => pem
                ? `sudo tee /usr/local/share/ca-certificates/opendeploy-ca.crt >/dev/null ${heredoc(pem)}\nsudo update-ca-certificates`
                : 'sudo cp opendeploy-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates'],
            ['Chrome / Chromium also', (pem) => pem
                ? `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" ${heredoc(pem)}`
                : 'certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" -i opendeploy-ca.crt'],
        ]},
        {key: 'windows', label: 'Windows (PowerShell)', steps: [
            ['Write the CA and trust it', (pem) => pem
                ? `@'\n${pem}\n'@ | Set-Content -Path opendeploy-ca.crt\ncertutil -addstore -f ROOT opendeploy-ca.crt`
                : 'certutil -addstore -f ROOT opendeploy-ca.crt'],
        ]},
        {key: 'firefox', label: 'Firefox', steps: [
            ['Use the OS store: set this to true in about:config', () => 'security.enterprise_roots.enabled'],
            ['Or import the downloaded file under', () => 'Settings › Privacy & Security › Certificates › View Certificates › Import'],
        ]},
    ];

    const tabButton = ({key, label}) => button({
        type: "button",
        role: "tab",
        "aria-selected": () => String(tab.val === key),
        class: () => `-mb-px border-b-2 px-2 py-1 text-xs cursor-pointer ${tab.val === key
            ? "border-brand text-gray-100"
            : "border-transparent text-gray-400 hover:text-gray-200"}`,
        onclick: () => { tab.val = key; },
    }, label);

    // Each command block carries its own copy button; the "Copied" state is
    // per block so copying one does not relabel the others. The block shows
    // the PEM collapsed to keep the tab short; the clipboard gets it in full.
    const cmd = (build) => {
        const done = van.state(false);
        const full = () => build(pemS.val);
        const shown = () => pemS.val ? full().replace(pemS.val, pemPlaceholder) : full();
        return div(
            {class: "flex items-start gap-1"},
            pre({class: "min-w-0 flex-1 whitespace-pre-wrap break-all rounded bg-gray-900 px-2 py-1 text-[11px] text-gray-300", "data-testid": "login-ca-cmd"}, shown),
            button({
                type: "button",
                class: "shrink-0 rounded border border-gray-600 px-2 py-1 text-[11px] text-gray-300 hover:bg-gray-700 cursor-pointer",
                title: "Copy command",
                "data-testid": "login-ca-cmd-copy",
                onclick: async () => {
                    try {
                        await navigator.clipboard.writeText(full());
                        done.val = true;
                        setTimeout(() => { done.val = false; }, 1500);
                    } catch (e) {
                        copyErr.val = `Copy failed: ${e.message}`;
                    }
                },
            }, () => done.val ? "Copied" : "Copy"),
        );
    };

    return details(
        {class: "min-w-0 rounded border border-gray-700 bg-gray-800/40 px-3 py-2 text-sm text-gray-300", "data-testid": "login-ca-help"},
        summary({class: "cursor-pointer select-none text-gray-400 hover:text-gray-200"}, "Browser warning about the certificate?"),
        div(
            {class: "mt-2 flex max-h-[50vh] min-w-0 flex-col gap-2 overflow-y-auto"},
            p({class: "text-xs text-gray-400"}, "This server has its own CA. Paste one command to trust it, restart the browser, reload."),
            div(
                {class: "flex flex-wrap items-center gap-2"},
                a({class: "rounded border border-gray-600 px-3 py-1 text-xs text-gray-200 hover:bg-gray-700 cursor-pointer no-underline", href: caURL, download: "opendeploy-ca.crt", "data-testid": "login-ca-download"}, "Download opendeploy-ca.crt"),
                button({type: "button", class: "rounded border border-gray-600 px-3 py-1 text-xs text-gray-200 hover:bg-gray-700 cursor-pointer", onclick: copyCA, "data-testid": "login-ca-copy"},
                    () => copied.val ? "Copied" : "Copy PEM"),
                () => copyErr.val ? span({class: "text-xs text-red-400"}, copyErr.val) : '',
            ),
            div({class: "flex flex-wrap gap-1 border-b border-gray-700", role: "tablist"}, ...platforms.map(tabButton)),
            () => {
                const active = platforms.find((p) => p.key === tab.val);
                return div({class: "flex flex-col gap-2"},
                    ...active.steps.map(([title, build]) => div(p({class: "text-xs text-gray-400"}, title), cmd(build))));
            },
            p({class: "text-xs text-red-400", "data-testid": "login-ca-warning"},
                "Careful: a trusted CA can sign certificates for any site. Only do this for a server you control, and only from a machine you trust. Remove the CA when you no longer need it."),
        ),
    );
}
