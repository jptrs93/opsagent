import van from "vanjs-core";
import {spinnerButton} from "./spinnerbutton.js";
import {inlineEditableInput} from "./inlineEditableInput.js";
import {explainable, explainerWidth, renderExplainer} from "./ruleExplainer.js";
import {describeGrant} from "../lib/authz.js";
import {grantSubject} from "../lib/authzExplain.js";
import {formatDate} from "../lib/date.js";
import {browserSupportsPasskeys} from "../util/webauthn.js";
import {closeIcon, eyeOffIcon, eyeOpenIcon, fingerprintIcon, keyRoundIcon, plusIcon} from "../lib/icons.js";

const {button, div, h2, input, p, span} = van.tags;

const generatePassword = () => {
    const bytes = new Uint8Array(18);
    crypto.getRandomValues(bytes);
    return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
};

const caption = (text) => span({class: "pt-1.5 text-[10px] font-semibold uppercase tracking-wider text-gray-500 slate:text-[11px] slate:font-medium slate:normal-case slate:tracking-normal slate:text-gray-400"}, text);
const heading = (title, hint) => div({class: "access-section-head"},
    span({class: "text-xs font-semibold text-gray-200"}, title),
    hint ? span({class: "text-[11px] text-gray-500"}, hint) : "",
    span({class: "access-section-rule"}));
const block = (...rows) => div({class: "access-block"}, ...rows);
const row = (label, ...children) => div({class: "grid gap-2 px-3 py-2.5 sm:grid-cols-[8rem_1fr] sm:items-start graphite:px-0 graphite:py-2"},
    caption(label),
    div({class: "flex min-w-0 flex-col gap-1.5"}, ...children));
const value = (text) => span({class: "text-sm text-gray-200"}, text);
const hint = (...children) => p({class: "text-[11px] text-gray-500"}, ...children);
const tag = (text, cls) => span({class: `shrink-0 rounded px-1.5 py-0.5 text-[10px] uppercase tracking-wide ${cls}`}, text);

const buttonBase = "inline-flex items-center justify-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium";
const primaryClass = "bg-brand text-white hover:bg-blue-600";
const secondaryClass = "border border-gray-600 text-gray-200 hover:bg-surface-hover slate:border-transparent slate:bg-white/[0.06] slate:hover:bg-white/10";
const actionButton = (text, onClick, {primary = false, disabledWhen, title, testid} = {}) => {
    const b = spinnerButton(text, onClick, primary ? primaryClass : secondaryClass, "button", disabledWhen, {base: buttonBase});
    if (title) b.title = title;
    if (testid) b.dataset.testid = testid;
    return b;
};

const passwordField = (state, placeholder, testid, autocomplete) => {
    const shown = van.state(false);
    return div({class: "relative"},
        input({
            class: "text-input text-sm",
            style: "padding-right: 2.25rem",
            type: () => shown.val ? "text" : "password",
            placeholder,
            autocomplete,
            "data-testid": testid,
            value: state,
            oninput: (e) => { state.val = e.target.value; },
        }),
        button({
            type: "button",
            tabindex: -1,
            title: () => shown.val ? "Hide" : "Show",
            class: "absolute right-2.5 top-1/2 -translate-y-1/2 cursor-pointer text-gray-500 hover:text-gray-200",
            onclick: () => { shown.val = !shown.val; },
        }, () => shown.val ? eyeOffIcon({class: "h-4 w-4"}) : eyeOpenIcon({class: "h-4 w-4"})));
};

// self() is the signed-in user with hasPassword and passkeys; actions:
// rename({name}), changePassword({current, next}), addPasskey() → passkey,
// removePasskey({id}).
export function accountPanel({self, users, grants, templates, spaces, spaceNames, actions, onManageSessions}) {
    const error = van.state("");
    const done = van.state("");
    const dialog = van.state(null);

    const templatesById = () => new Map((templates() || []).map((t) => [Number(t.id), t]));
    const me = () => self() || {id: 0, name: "", passkeys: []};
    const displayName = (user) => user?.name || `user ${user?.id}`;

    const run = async (work, success) => {
        error.val = "";
        done.val = "";
        try {
            await work();
            if (success) done.val = success;
        } catch (e) {
            error.val = e?.message || "Something went wrong.";
        }
    };

    // --- identity --------------------------------------------------------------

    const nameDraft = van.state(null);
    const draftName = () => (nameDraft.val ?? me().name).trim();
    const nameTaken = () => {
        const wanted = draftName();
        return Boolean(wanted) && (users() || []).some((u) => Number(u.id) !== Number(me().id) && (u.name || "").trim() === wanted);
    };
    const nameDirty = () => nameDraft.val !== null && draftName() !== (me().name || "").trim();

    const identitySection = () => div({class: "flex flex-col gap-2"},
        heading("Identity"),
        block(
            row("Username",
                inlineEditableInput({
                    value: () => nameDraft.val ?? me().name,
                    oninput: (e) => { nameDraft.val = e.target.value; },
                    dirty: nameDirty,
                    valid: () => Boolean(draftName()) && !nameTaken(),
                    onSave: () => run(async () => {
                        await actions.rename({name: draftName()});
                        nameDraft.val = null;
                    }, "Username changed."),
                    onDiscard: () => { nameDraft.val = null; },
                    inputClass: "input text-sm",
                    inputAttrs: {"data-testid": "account-name-input", autocomplete: "off"},
                    ariaLabel: "Username",
                    saveAriaLabel: "Save username",
                    discardAriaLabel: "Discard username change",
                }),
                () => nameTaken()
                    ? p({class: "text-[11px] text-amber-300", "data-testid": "account-name-taken"}, `A user named ${draftName()} already exists.`)
                    : nameDirty() ? hint("Enter saves, Escape discards. Sign-in links and passkeys stay valid.") : hint("Shown wherever you are the author of a change.")),
            row("User id", value(() => String(me().id))),
            row("Joined", value(() => formatDate(new Date(Number(me().createdAt || 0)), "—"))),
        ));

    // --- sign in -------------------------------------------------------------------

    const passkeyRow = (passkey) => {
        const last = me().passkeys.length === 1 && !me().hasPassword;
        return div({class: "access-inset flex items-center gap-3 rounded-md border px-3 py-2", "data-testid": `account-passkey-${passkey.id}`},
            fingerprintIcon({class: "h-4 w-4 flex-none text-gray-400"}),
            div({class: "min-w-0 flex-1"},
                div({class: "truncate text-sm text-gray-200"}, passkey.label || "Passkey"),
                div({class: "text-[11px] text-gray-500"},
                    `Added ${formatDate(new Date(Number(passkey.createdAt || 0)), "—")}`,
                    passkey.lastUsedAt ? ` · last used ${formatDate(new Date(Number(passkey.lastUsedAt)), "—")}` : " · never used")),
            last
                ? span({class: "inline-flex h-6 w-6 items-center justify-center rounded text-gray-600 cursor-not-allowed", title: "Your only way to sign in. Set a password or add another passkey first."}, closeIcon({class: "h-3.5 w-3.5"}))
                : button({
                    type: "button",
                    title: "Remove passkey",
                    "aria-label": `Remove ${passkey.label || "passkey"}`,
                    class: "inline-flex h-6 w-6 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-surface slate:hover:bg-white/[0.06] graphite:hover:bg-white/[0.06] hover:text-red-400",
                    onclick: () => run(() => actions.removePasskey({id: passkey.id}), "Passkey removed."),
                }, closeIcon({class: "h-3.5 w-3.5"})));
    };

    const signInSection = () => div({class: "flex flex-col gap-2"},
        heading("Sign in"),
        block(
            row("Password",
                div({class: "flex flex-wrap items-center gap-3"},
                    () => me().hasPassword
                        ? value("Set")
                        : span({class: "text-sm text-gray-400"}, "Not set"),
                    () => actionButton(me().hasPassword ? "Change password" : "Set a password", () => { dialog.val = "password"; }, {testid: "account-password-open"})),
                () => me().hasPassword
                    ? hint("Used with your username on the sign-in page.")
                    : hint("Without one you sign in with a passkey only. A password also works where passkeys do not, such as plain HTTP away from localhost.")),
            row("Passkeys",
                () => {
                    const list = me().passkeys || [];
                    return list.length
                        ? div({class: "flex flex-col gap-1.5"}, ...list.map(passkeyRow))
                        : p({class: "text-sm text-gray-400"}, "None registered.");
                },
                div({class: "flex flex-wrap items-center gap-3"},
                    browserSupportsPasskeys()
                        ? actionButton(span({class: "inline-flex items-center gap-1.5"}, plusIcon({class: "h-3.5 w-3.5"}), "Add passkey"), () => run(() => actions.addPasskey(), "Passkey added."), {testid: "account-passkey-add"})
                        : span({class: "text-xs text-gray-500"}, "This browser cannot register passkeys here. Passkeys need HTTPS with a trusted certificate, or localhost."),
                    hint("A passkey is stored by your browser or password manager and unlocked with Touch ID, Windows Hello, or a security key."))),
            row("Sessions",
                div({class: "flex flex-wrap items-center gap-3"},
                    value("Everywhere you are signed in"),
                    actionButton("Manage sessions", () => onManageSessions?.(), {testid: "account-sessions"})),
                hint("Sign out a lost device, or everything but this browser.")),
        ));

    // --- access --------------------------------------------------------------------

    const accessChip = (grant) => {
        const chip = describeGrant(grant, templatesById(), spaceNames());
        const el = span(
            {class: `access-chip ${chip.template ? "access-chip-role" : "access-chip-direct"}`},
            span({class: "access-chip-label"}, chip.label),
            chip.detail ? span({class: "access-chip-detail"}, chip.template ? `(${chip.detail})` : chip.detail) : "");
        const subject = () => grantSubject(grant, {templatesById: templatesById(), spaceNames: spaceNames(), spaces: spaces()});
        return explainable(el, (open) => renderExplainer(subject(), open), {width: () => explainerWidth(subject()), label: `Explain ${chip.label}`});
    };

    const accessSection = () => div({class: "flex flex-col gap-2"},
        heading("Your access", "hover a grant to see exactly what it allows"),
        block(row("Grants",
            () => {
                const own = (grants() || []).filter((g) => Number(g.userId) === Number(me().id));
                return own.length
                    ? div({class: "flex flex-wrap items-center gap-1.5"}, ...own.map(accessChip))
                    : p({class: "text-sm text-gray-400"}, "None. You can only do what the global rules allow everyone.");
            },
            hint("Only someone who manages access can change these."))));

    // --- password dialog -----------------------------------------------------------

    const passwordDialog = () => {
        const current = van.state("");
        const next = van.state("");
        const saving = van.state(false);
        const err = van.state("");
        const needsCurrent = me().hasPassword;
        const close = () => { dialog.val = null; };
        const save = async () => {
            if (saving.val) return;
            saving.val = true;
            err.val = "";
            try {
                await actions.changePassword({current: current.val, next: next.val});
                close();
                done.val = needsCurrent ? "Password changed." : "Password set.";
            } catch (e) {
                err.val = e?.message || "Something went wrong.";
            } finally {
                saving.val = false;
            }
        };
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", onkeydown: (e) => { if (e.key === "Escape") { e.preventDefault(); close(); } }},
            div({class: "dialog access-dialog flex w-full max-w-md flex-col gap-4 p-4", role: "dialog", "aria-modal": "true", "aria-label": needsCurrent ? "Change password" : "Set a password", "data-testid": "account-password-dialog"},
                div({class: "flex items-center justify-between gap-4"},
                    h2({class: "text-base font-semibold"}, needsCurrent ? "Change password" : "Set a password"),
                    button({type: "button", title: "Close", "aria-label": "Close", class: "inline-flex h-6 w-6 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-surface-hover hover:text-gray-100", onclick: close}, closeIcon({class: "h-3.5 w-3.5"}))),
                needsCurrent ? div({class: "flex flex-col gap-1"}, caption("Current password"), passwordField(current, "Current password", "account-password-current", "current-password")) : "",
                div({class: "flex flex-col gap-1"},
                    caption("New password"),
                    div({class: "flex items-center gap-2"},
                        div({class: "min-w-0 flex-1"}, passwordField(next, "New password", "account-password-next", "new-password")),
                        button({
                            type: "button",
                            class: "inline-flex h-[38px] flex-none cursor-pointer items-center rounded-lg border border-gray-600 px-3 text-xs text-gray-200 hover:bg-surface-hover",
                            onclick: () => { next.val = generatePassword(); },
                        }, "Generate")),
                    hint("Other sessions stay signed in. Sign them out under Manage sessions if the old password may be known.")),
                () => err.val ? p({class: "text-sm text-red-400"}, err.val) : "",
                div({class: "flex items-center justify-end gap-2"},
                    button({type: "button", class: "cursor-pointer text-xs text-gray-400 hover:text-gray-200", onclick: close}, "Cancel"),
                    actionButton(needsCurrent ? "Change password" : "Set password", save, {
                        primary: true,
                        disabledWhen: () => saving.val || !next.val || (needsCurrent && !current.val),
                        testid: "account-password-save",
                    }))));
    };

    // --- page ----------------------------------------------------------------------

    return div({class: "app-scroll flex h-full min-h-0 flex-col overflow-y-auto", "data-testid": "account-panel"},
        div({class: "flex w-full max-w-3xl flex-col gap-6 px-4 py-4"},
            div({class: "flex items-center gap-3"},
                span({class: "flex h-9 w-9 items-center justify-center rounded-full border border-gray-700 bg-gray-900 text-gray-300 slate:border-line slate:bg-white/[0.04] graphite:border-line graphite:bg-frame"}, keyRoundIcon({class: "h-4 w-4"})),
                div({class: "min-w-0"},
                    div({class: "flex items-center gap-2"},
                        h2({class: "truncate text-base font-semibold text-white"}, () => displayName(me())),
                        () => me().pending ? tag("pending", "bg-amber-500/15 text-amber-300") : ""),
                    p({class: "text-xs text-gray-500"}, "Your account. What you see here is yours to change; your access is not."))),
            () => error.val ? p({class: "rounded-md border border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"}, error.val) : "",
            () => done.val ? p({class: "rounded-md border border-emerald-900/60 bg-emerald-950/30 px-3 py-1.5 text-xs text-emerald-200/90"}, done.val) : "",
            identitySection(),
            signInSection(),
            accessSection()),
        () => dialog.val === "password" ? passwordDialog() : "");
}
