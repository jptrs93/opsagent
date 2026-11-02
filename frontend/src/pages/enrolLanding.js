import van from "vanjs-core";
import {navigate} from "../lib/router.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {authButtonBase, authButtonClass, authCard, authLinkClass, fieldGroup, fieldInput, fieldRow, footerBand, footerText, notice, revealToggle, withIcon} from "../components/authCard.js";
import {capi} from "../capi/index.js";
import {loginS, setLoginFromResponse} from "../state/login.js";
import {authMethodsS, loadAuthMethods} from "../state/authMethods.js";
import {loadAccount, registerPasskey} from "../state/account.js";
import {isRootSession} from "../lib/userSessions.js";
import {browserSupportsPasskeys, passkeyNotAllowedMessage, passkeyServerErrorMessage} from "../util/webauthn.js";
import {keyRoundIcon} from "../lib/icons.js";

const {a, div, form, p} = van.tags;

// Where an enrolment or recovery link lands: the token is redeemed once for
// a session, then the page asks for whatever the account still lacks (a
// username, a password), offers a passkey, and goes home. A spent or
// expired link shows why and points at the login page.
export function enrolLandingPage(token) {
    const status = van.state("");
    const phase = van.state("redeeming");
    const needs = van.state({name: false, password: false});
    loadAuthMethods();

    const steps = () => {
        const list = [];
        if (needs.val.name) list.push("name");
        if (needs.val.password) list.push("password");
        return list;
    };
    const next = () => {
        const remaining = steps();
        if (remaining.length) phase.val = remaining[0];
        else if (browserSupportsPasskeys() && authMethodsS.val.passkeyLoginEnabled) phase.val = "passkey";
        else navigate("/");
    };

    const start = async () => {
        try {
            if (loginS.val && !isRootSession(loginS.val)) {
                const account = await loadAccount();
                needs.val = {name: !account.name, password: !account.hasPassword};
            } else {
                const response = await capi.postV1AuthLinkRedeem({token});
                setLoginFromResponse(response.session);
                needs.val = {name: !response.nameSet, password: !response.passwordSet};
            }
            next();
        } catch (err) {
            phase.val = "failed";
            status.val = notice("error", "enrol-landing-error", err?.message || "This link is not valid any more.");
        }
    };
    queueMicrotask(start);

    const nameStep = () => {
        const nameInput = fieldInput({type: "text", "data-testid": "landing-name-input", required: true, placeholder: "Choose a username", autocomplete: "username"});
        const submit = spinnerButton("Continue", null, authButtonClass, "submit", null, {base: authButtonBase});
        submit.dataset.testid = "landing-name-continue";
        return form({
            class: "flex flex-col gap-4",
            onsubmit: async (e) => {
                e.preventDefault();
                submit.isSubmitting.val = true;
                status.val = "";
                try {
                    await capi.postV1AccountRename({name: nameInput.value.trim()});
                    needs.val = {...needs.val, name: false};
                    next();
                } catch (err) {
                    status.val = notice("error", "enrol-landing-error", err?.message || "Could not set the username.");
                } finally {
                    submit.isSubmitting.val = false;
                }
            },
        },
        p({class: "text-sm text-gray-400"}, "You are signed in. Pick the username you will sign in with from now on."),
        fieldGroup(fieldRow("Username", nameInput)),
        submit);
    };

    const passwordStep = () => {
        const passwordInput = fieldInput({type: "password", "data-testid": "landing-password-input", required: true, minlength: 8, placeholder: "At least 8 characters", autocomplete: "new-password"});
        const submit = spinnerButton("Continue", null, authButtonClass, "submit", null, {base: authButtonBase});
        submit.dataset.testid = "landing-password-continue";
        return form({
            class: "flex flex-col gap-4",
            onsubmit: async (e) => {
                e.preventDefault();
                submit.isSubmitting.val = true;
                status.val = "";
                try {
                    await capi.postV1AccountPassword({next: passwordInput.value});
                    passwordInput.value = "";
                    needs.val = {...needs.val, password: false};
                    next();
                } catch (err) {
                    status.val = notice("error", "enrol-landing-error", err?.message || "Could not set the password.");
                } finally {
                    submit.isSubmitting.val = false;
                }
            },
        },
        p({class: "text-sm text-gray-400"}, "Set the password you will sign in with. You can change it later under Users & roles › You."),
        fieldGroup(fieldRow("Password", passwordInput, revealToggle(passwordInput))),
        submit);
    };

    const passkeyStep = () => {
        const register = spinnerButton(withIcon(keyRoundIcon({class: "h-4 w-4"}), "Add a passkey"), async () => {
            status.val = "";
            try {
                await registerPasskey();
                navigate("/");
            } catch (e) {
                status.val = notice("error", "enrol-landing-error", e?.name === "NotAllowedError"
                    ? passkeyNotAllowedMessage("Passkey registration", e)
                    : passkeyServerErrorMessage("Passkey registration", e));
            }
        }, authButtonClass, "button", null, {base: authButtonBase});
        register.dataset.testid = "landing-passkey-register";
        return div({class: "flex flex-col gap-4"},
            notice("success", "landing-ready", "You are all set. A passkey lets you sign in with Touch ID, Windows Hello, or a security key instead of a password."),
            register,
            p({class: "text-center text-sm"}, a({class: authLinkClass, "data-testid": "landing-skip", onclick: () => navigate("/")}, "Skip for now")));
    };

    const body = () => {
        switch (phase.val) {
            case "redeeming": return p({class: "text-sm text-gray-400", "data-testid": "landing-redeeming"}, "Signing you in…");
            case "name": return nameStep();
            case "password": return passwordStep();
            case "passkey": return passkeyStep();
            default: return p({class: "text-sm text-gray-400"}, "Ask whoever sent you the link for a new one.");
        }
    };

    return authCard({
        title: "Welcome",
        body: [() => status.val || "", body],
        footer: () => footerBand(
            footerText("Already have access? ", a({class: authLinkClass, "data-testid": "landing-back-to-login", onclick: () => navigate("/login")}, "Go to login")),
        ),
    });
}
