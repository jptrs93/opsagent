import van from "vanjs-core";
import {spinnerButton} from "./spinnerbutton.js";
import {grantEditor, menuBackdrop} from "./accessEditors.js";
import {describeGrant} from "../lib/authz.js";
import {formatDate} from "../lib/date.js";
import {checkIcon, closeIcon, copyIcon, eyeOffIcon, eyeOpenIcon, keyRoundIcon, lifeBuoyIcon, linkIcon, plusIcon, searchIcon, userPlusIcon} from "../lib/icons.js";

const {button, div, h2, input, p, span} = van.tags;

export const ENROL_LINK_DAYS = 7;

const generatePassword = () => {
    const bytes = new Uint8Array(18);
    crypto.getRandomValues(bytes);
    return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
};

const caption = (text) => span({class: "text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, text);

const section = (title, hint, ...children) => div({class: "flex flex-col gap-2"},
    div({class: "flex flex-wrap items-baseline gap-x-2 gap-y-0.5 border-b border-gray-800 pb-1"},
        span({class: "text-xs font-semibold text-gray-200"}, title),
        hint ? span({class: "text-[11px] text-gray-500"}, hint) : ""),
    ...children);

const buttonBase = "inline-flex items-center justify-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium";
const primaryClass = "bg-brand text-white hover:bg-blue-600";
const secondaryClass = "border border-gray-600 text-gray-200 hover:bg-surface-hover";

const actionButton = (text, onClick, {primary = false, disabledWhen, title, testid} = {}) => {
    const b = spinnerButton(text, onClick, primary ? primaryClass : secondaryClass, "button", disabledWhen, {base: buttonBase});
    if (title) b.title = title;
    if (testid) b.dataset.testid = testid;
    return b;
};

const quietButton = (text, onclick, testid) => button({
    type: "button",
    class: "cursor-pointer text-xs text-gray-400 hover:text-gray-200",
    "data-testid": testid,
    onclick,
}, text);

const segment = (state, value, text) => button({
    type: "button",
    class: () => `rounded px-2.5 py-1 text-xs border cursor-pointer transition-colors ` +
        (state.val === value ? "border-brand text-gray-100 bg-brand/15" : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
    onclick: () => { state.val = value; },
}, text);

const textField = (attrs) => input({class: "text-input text-sm", ...attrs});

const tag = (text, cls) => span({class: `shrink-0 rounded px-1.5 py-0.5 text-[10px] uppercase tracking-wide ${cls}`}, text);

const grantChip = (chip, onRemove) => span(
    {class: `inline-flex items-center gap-1.5 rounded border px-1.5 py-px text-[11px] ` +
        (chip.template ? "border-blue-500/30 bg-blue-500/10 text-gray-300" : "border-gray-700 bg-gray-950/40 text-gray-400")},
    span({class: `font-medium ${chip.template ? "text-blue-300" : "text-gray-200"}`}, chip.label),
    chip.detail ? span({class: "text-gray-400"}, chip.template ? `(${chip.detail})` : chip.detail) : "",
    onRemove ? button({
        type: "button",
        title: "Remove",
        "aria-label": `Remove ${chip.label}`,
        class: "inline-flex h-3.5 w-3.5 items-center justify-center rounded text-gray-500 hover:text-red-400 cursor-pointer",
        onclick: onRemove,
    }, closeIcon({class: "w-3 h-3"})) : "",
);

const copyButton = (value) => {
    const copied = van.state(false);
    return button({
        type: "button",
        class: "inline-flex h-7 flex-none cursor-pointer items-center gap-1 rounded-md border border-gray-600 px-2 text-[11px] text-gray-200 hover:bg-surface-hover",
        "data-testid": "enrol-copy-link",
        onclick: async () => {
            try { await navigator.clipboard.writeText(value); } catch {}
            copied.val = true;
            setTimeout(() => { copied.val = false; }, 1500);
        },
    }, () => copied.val ? checkIcon({class: "h-3 w-3 text-green-400"}) : copyIcon({class: "h-3 w-3"}), () => copied.val ? "Copied" : "Copy");
};

const notice = (tone, ...children) => div(
    {class: `flex items-start gap-2 rounded-lg border px-3 py-2.5 text-sm ${tone === "ok"
        ? "border-emerald-900/60 bg-emerald-950/30 text-emerald-200/90"
        : "border-red-900/60 bg-red-950/40 text-red-300"}`},
    span({class: "min-w-0"}, ...children));

// actions: createUser({name, password, grants}) → user, createLink({userId}) → {url, expiresAt}, signIn({userId, password}).
export function userEnrolWidget({
    context,
    actions,
    users,
    grants,
    templates,
    spaces,
    spaceNames,
    initialPath = null,
    onClose,
    onDone,
    onSignedIn,
}) {
    const path = van.state(initialPath || "choose");
    const result = van.state(null);
    const error = van.state("");
    const busy = van.state(false);
    const openMenu = van.state(null);

    const templatesById = () => new Map((templates() || []).map((t) => [Number(t.id), t]));
    const describe = (source) => describeGrant({grant: source}, templatesById(), spaceNames());
    const displayName = (user) => user?.name || `user ${user?.id}`;

    const title = van.derive(() => {
        const r = result.val;
        if (r) return r.kind === "link" ? (r.recovery ? "Recovery link" : "Sign-in link") : "User created";
        if (path.val === "enrol") return "Enrol a new user";
        if (path.val === "recover") return "Recover a user";
        return "Enrol or recover";
    });
    const phase = van.derive(() => result.val ? "done" : "form");

    const run = async (work) => {
        if (busy.val) return;
        busy.val = true;
        error.val = "";
        try {
            await work();
        } catch (e) {
            error.val = e?.message || "Something went wrong.";
        } finally {
            busy.val = false;
        }
    };

    // --- choose ------------------------------------------------------------------

    const choice = (icon, heading, text, onclick, testid) => button({
        type: "button",
        class: "group flex w-full cursor-pointer items-start gap-3 rounded-lg border border-gray-700 bg-gray-900/40 px-4 py-3 text-left transition-colors hover:border-brand/70 hover:bg-brand/10",
        "data-testid": testid,
        onclick,
    },
    span({class: "mt-0.5 flex h-8 w-8 flex-none items-center justify-center rounded-md border border-gray-700 bg-gray-950/60 text-gray-300 transition-colors group-hover:border-brand/50 group-hover:text-blue-200"}, icon),
    span({class: "min-w-0"},
        span({class: "block text-sm font-medium text-gray-100"}, heading),
        span({class: "mt-0.5 block text-xs leading-relaxed text-gray-400"}, text)));

    const choosePanel = () => div({class: "flex flex-col gap-3"},
        choice(userPlusIcon({class: "h-4 w-4"}), "Enrol a new user",
            context === "login"
                ? "Create the account and choose what it can do, then hand over a sign-in link or sign in as them straight away."
                : "Create the account and choose what it can do, then hand over a sign-in link.",
            () => { path.val = "enrol"; }, "enrol-choose-new"),
        choice(lifeBuoyIcon({class: "h-4 w-4"}), "Recover an existing user",
            context === "login"
                ? "Give someone a way back into their account: a one-time link, or sign in as them now. Nothing about the account changes until they are back in."
                : "Give someone a way back into their account with a one-time link. Nothing about the account changes until they are back in.",
            () => { path.val = "recover"; }, "enrol-choose-recover"),
    );

    // --- enrol -------------------------------------------------------------------

    const name = van.state("");
    const passwordMode = van.state("link");
    const password = van.state("");
    const passwordShown = van.state(false);
    const grantList = van.state([]);
    const adding = van.state(false);

    const nameTaken = () => {
        const wanted = name.val.trim();
        return Boolean(wanted) && (users() || []).some((u) => (u.name || "").trim() === wanted);
    };
    const canDirect = () => Boolean(name.val.trim()) && !nameTaken() && passwordMode.val === "set" && Boolean(password.val);
    const canLink = () => !nameTaken() && (passwordMode.val === "link" || canDirect());

    const detailRow = (label, ...children) => div({class: "grid gap-2 px-3 py-2.5 sm:grid-cols-[7rem_1fr] sm:items-start"},
        span({class: "pt-2 text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, label),
        div({class: "flex min-w-0 flex-col gap-1.5"}, ...children));

    const detailsSection = () => {
        const nameInput = textField({
            type: "text",
            placeholder: "e.g. alice",
            autocomplete: "off",
            "data-testid": "enrol-name-input",
            value: name,
            oninput: (e) => { name.val = e.target.value; },
        });
        const passwordInput = textField({
            type: () => passwordShown.val ? "text" : "password",
            style: "padding-right: 2.25rem",
            placeholder: "Password",
            autocomplete: "new-password",
            "data-testid": "enrol-password-input",
            value: password,
            oninput: (e) => { password.val = e.target.value; },
        });
        return section("User details", null,
            div({class: "divide-y divide-gray-800 rounded-md border border-gray-700/60 bg-gray-950/30"},
                detailRow("Username",
                    nameInput,
                    () => nameTaken()
                        ? p({class: "text-[11px] text-amber-300", "data-testid": "enrol-name-taken"}, `A user named ${name.val.trim()} already exists.`)
                        : passwordMode.val === "link" && !name.val.trim()
                            ? p({class: "text-[11px] text-gray-500"}, "Leave it blank and they pick a name when they open the link.")
                            : ""),
                detailRow("Password",
                    div({class: "flex items-center gap-1.5"},
                        segment(passwordMode, "link", "They choose it"),
                        segment(passwordMode, "set", "Set it now")),
                    () => passwordMode.val !== "set" ? "" : div({class: "flex items-center gap-2"},
                        div({class: "relative min-w-0 flex-1"},
                            passwordInput,
                            button({
                                type: "button",
                                tabindex: -1,
                                title: () => passwordShown.val ? "Hide password" : "Show password",
                                class: "absolute right-2.5 top-1/2 -translate-y-1/2 cursor-pointer text-gray-500 hover:text-gray-200",
                                onclick: () => { passwordShown.val = !passwordShown.val; },
                            }, () => passwordShown.val ? eyeOffIcon({class: "h-4 w-4"}) : eyeOpenIcon({class: "h-4 w-4"}))),
                        button({
                            type: "button",
                            class: "inline-flex h-[38px] flex-none cursor-pointer items-center rounded-lg border border-gray-600 px-3 text-xs text-gray-200 hover:bg-surface-hover",
                            "data-testid": "enrol-generate-password",
                            onclick: () => { password.val = generatePassword(); passwordShown.val = true; },
                        }, "Generate")),
                    () => p({class: "text-[11px] text-gray-500"}, passwordMode.val === "set"
                        ? "You hand the password over yourself. They can change it once signed in."
                        : "They set one when they open the link."))));
    };

    const addPanel = () => {
        const editor = grantEditor({templates, spaces, spaceNames, openMenu, menuPrefix: "enrol"});
        const addError = van.state("");
        return div({class: "flex flex-col gap-3 rounded-md border border-gray-700/60 bg-gray-950/30 p-3", "data-testid": "enrol-add-grant"},
            editor.node,
            () => addError.val ? p({class: "text-xs text-red-400"}, addError.val) : "",
            div({class: "flex items-center justify-end gap-2"},
                quietButton("Cancel", () => { adding.val = false; }),
                actionButton("Add", () => {
                    try {
                        const source = editor.build();
                        grantList.val = [...grantList.val, {source}];
                        adding.val = false;
                    } catch (e) {
                        addError.val = e.message;
                    }
                }, {primary: true, testid: "enrol-add-grant-confirm"})));
    };

    const privilegesSection = () => section("Privileges", "what the account is allowed to do",
        () => {
            const list = grantList.val;
            if (!list.length) {
                return p({class: "text-xs text-gray-500"},
                    "None yet. They will be able to sign in, but only see what the global rules allow everyone.");
            }
            return div({class: "flex flex-wrap items-center gap-1.5"},
                ...list.map((entry) => grantChip(describe(entry.source), () => {
                    grantList.val = grantList.val.filter((g) => g !== entry);
                })));
        },
        () => adding.val ? addPanel() : button({
            type: "button",
            class: "inline-flex w-max cursor-pointer items-center gap-1 rounded-md border border-dashed border-gray-600 px-2 py-1 text-xs text-gray-300 hover:border-gray-400 hover:text-gray-100",
            "data-testid": "enrol-add-grant-open",
            onclick: () => { adding.val = true; },
        }, plusIcon({class: "w-3 h-3"}), "Add privilege"),
    );

    const finishEnrol = (how) => run(async () => {
        const created = await actions.createUser({
            name: name.val.trim(),
            password: passwordMode.val === "set" ? password.val : "",
            grants: grantList.val.map((g) => g.source),
        });
        if (how === "link") {
            const link = await actions.createLink({userId: created.id});
            result.val = {kind: "link", recovery: false, user: created, ...link};
        } else if (context === "login") {
            await actions.signIn({userId: created.id, password: password.val});
            onSignedIn?.(created);
        } else {
            result.val = {kind: "created", user: created};
        }
    });

    const enrolPanel = () => div({class: "flex flex-col gap-5"},
        privilegesSection(),
        detailsSection(),
        actionRow(
            context === "login"
                ? actionButton("Create and sign in", () => finishEnrol("direct"), {
                    disabledWhen: () => busy.val || !canDirect(),
                    title: "Needs a username and a password set now",
                    testid: "enrol-sign-in",
                })
                : actionButton("Create user", () => finishEnrol("direct"), {
                    disabledWhen: () => busy.val || !canDirect(),
                    title: "Needs a username and a password set now",
                    testid: "enrol-create",
                }),
            actionButton(span({class: "inline-flex items-center gap-1.5"}, linkIcon({class: "h-3.5 w-3.5"}), "Create sign-in link"), () => finishEnrol("link"), {
                primary: true,
                disabledWhen: () => busy.val || !canLink(),
                testid: "enrol-create-link",
            }),
        ),
    );

    // --- recover -----------------------------------------------------------------

    const search = van.state("");
    const selectedId = van.state(0);
    const selectedUser = () => (users() || []).find((u) => Number(u.id) === Number(selectedId.val)) || null;

    const userList = () => {
        const query = search.val.trim().toLowerCase();
        const all = [...(users() || [])].sort((a, b) => Number(a.id) - Number(b.id));
        const visible = query ? all.filter((u) => displayName(u).toLowerCase().includes(query) || String(u.id) === query) : all;
        if (!visible.length) return p({class: "px-3 py-3 text-sm text-gray-500"}, query ? "No users match." : "No users yet.");
        return div({class: "flex flex-col"}, ...visible.map((user) => button({
            type: "button",
            class: () => `flex w-full items-center gap-3 px-3 py-1.5 text-left text-[13px] cursor-pointer ` +
                (Number(selectedId.val) === Number(user.id) ? "bg-brand/15 text-gray-100" : "text-gray-300 hover:bg-surface-hover"),
            "data-testid": `enrol-recover-user-${user.id}`,
            onclick: () => { selectedId.val = Number(user.id); },
        },
        span({class: "min-w-0 flex-1 truncate"}, displayName(user)),
        user.pending ? tag("pending", "bg-amber-500/15 text-amber-300") : "",
        span({class: "w-8 text-right tabular-nums text-gray-500"}, String(user.id)),
        span({class: "hidden w-24 text-right text-gray-500 sm:block"}, formatDate(new Date(Number(user.createdAt || 0)), "—")))));
    };

    const summary = () => {
        const user = selectedUser();
        if (!user) return p({class: "text-xs text-gray-500"}, "Pick the account to recover. Its name and privileges stay as they are.");
        const own = (grants() || []).filter((g) => Number(g.userId) === Number(user.id));
        return div({class: "flex flex-col gap-2 rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2.5", "data-testid": "enrol-recover-summary"},
            div({class: "flex items-center gap-2"},
                span({class: "text-sm font-medium text-gray-100"}, displayName(user)),
                span({class: "text-xs text-gray-500"}, `id ${user.id}`),
                user.createdAt ? span({class: "text-xs text-gray-500"}, `· joined ${formatDate(new Date(Number(user.createdAt)), "—")}`) : ""),
            div({class: "flex flex-wrap items-center gap-1.5"},
                ...(own.length ? own.map((g) => grantChip(describe(g.grant))) : [span({class: "text-xs text-gray-500"}, "No privileges")])));
    };

    const finishRecover = (how) => run(async () => {
        const user = selectedUser();
        if (!user) throw new Error("Choose a user");
        if (how === "link") {
            const link = await actions.createLink({userId: user.id});
            result.val = {kind: "link", recovery: true, user, ...link};
        } else {
            await actions.signIn({userId: user.id});
            onSignedIn?.(user);
        }
    });

    const recoverPanel = () => div({class: "flex flex-col gap-5"},
        div({class: "flex flex-col gap-2"},
            div({class: "overflow-hidden rounded-md border border-gray-700/60"},
                div({class: "relative border-b border-gray-800"},
                    searchIcon({class: "pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
                    input({
                        class: "w-full bg-transparent py-2 pl-8 pr-3 text-sm text-gray-100 placeholder-gray-600 focus:outline-none",
                        type: "search",
                        placeholder: "Search users",
                        "data-testid": "enrol-recover-search",
                        value: search,
                        oninput: (e) => { search.val = e.target.value; },
                    })),
                div({class: "app-scroll max-h-44 overflow-y-auto"}, userList)),
            summary),
        actionRow(
            context === "login"
                ? actionButton(() => `Sign in as ${selectedUser() ? displayName(selectedUser()) : "…"}`, () => finishRecover("direct"), {
                    disabledWhen: () => busy.val || !selectedUser(),
                    testid: "enrol-recover-sign-in",
                })
                : "",
            actionButton(span({class: "inline-flex items-center gap-1.5"}, linkIcon({class: "h-3.5 w-3.5"}), "Create recovery link"), () => finishRecover("link"), {
                primary: true,
                disabledWhen: () => busy.val || !selectedUser(),
                testid: "enrol-recover-link",
            }),
        ),
    );

    // --- result ------------------------------------------------------------------

    const resultPanel = () => {
        const r = result.val;
        const who = displayName(r.user);
        if (r.kind === "created") {
            return div({class: "flex flex-col gap-4"},
                notice("ok", span({class: "font-medium"}, who), " can sign in with the username and password you set. The account shows as pending until they do."),
                actionRow(quietButton("Enrol another", reset, "enrol-another"), actionButton("Done", () => onDone?.(r), {primary: true, testid: "enrol-done"})));
        }
        const days = Math.max(1, Math.round((Number(r.expiresAt) - Date.now()) / 864e5));
        return div({class: "flex flex-col gap-4"},
            notice("ok", span({class: "font-medium"}, who), r.recovery
                ? " can use this link once to get back into their account."
                : " is enrolled. Send them this link; it signs them in once and lets them finish setting up."),
            div({class: "flex flex-col gap-1.5"},
                caption("Link"),
                div({class: "flex items-center gap-2 rounded-lg border border-gray-700 bg-gray-950/60 py-1.5 pl-3 pr-1.5"},
                    span({class: "min-w-0 flex-1 truncate font-mono text-xs text-gray-200", "data-testid": "enrol-link"}, r.url),
                    copyButton(r.url)),
                p({class: "text-[11px] text-gray-500"},
                    `Works once and expires in ${days} day${days === 1 ? "" : "s"}. Anyone holding it can sign in as ${who}, so share it privately.`)),
            actionRow(
                context === "users" ? quietButton("Enrol another", reset, "enrol-another") : "",
                actionButton("Done", () => onDone?.(r), {primary: true, testid: "enrol-done"})));
    };

    // --- frame -------------------------------------------------------------------

    function actionRow(...buttons) {
        return div({class: "flex flex-wrap items-center gap-2 border-t border-gray-800 pt-4"},
            () => path.val === "choose" || result.val ? "" : (initialPath
                ? (onClose ? quietButton("Cancel", onClose, "enrol-cancel") : "")
                : quietButton("‹ Back", () => { path.val = "choose"; error.val = ""; }, "enrol-back")),
            div({class: "flex-1"}),
            ...buttons);
    }

    function reset() {
        result.val = null;
        error.val = "";
        name.val = "";
        password.val = "";
        passwordMode.val = "link";
        grantList.val = [];
        adding.val = false;
        selectedId.val = 0;
        search.val = "";
        path.val = initialPath || "choose";
    }

    const node = div({class: "flex flex-col gap-4", "data-testid": "enrol-widget"},
        () => error.val ? notice("error", error.val) : "",
        () => {
            if (result.val) return resultPanel();
            if (path.val === "enrol") return enrolPanel();
            if (path.val === "recover") return recoverPanel();
            return div({class: "flex flex-col gap-3"},
                choosePanel(),
                onClose ? div({class: "flex justify-end"}, quietButton("Cancel", onClose, "enrol-cancel")) : "");
        },
        menuBackdrop(openMenu),
    );

    return {node, title, phase};
}

export function userEnrolOverlay({onClose, ...rest}) {
    const widget = userEnrolWidget({context: "users", onClose, onDone: onClose, ...rest});
    return div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "flex w-full max-w-2xl max-h-[88vh] flex-col overflow-hidden rounded-[0.3rem] border border-gray-700 bg-surface shadow-2xl", "data-testid": "enrol-overlay"},
            div({class: "flex flex-none items-center justify-between gap-3 border-b border-gray-800 px-4 py-3"},
                h2({class: "text-base font-semibold"}, () => widget.title.val),
                button({
                    type: "button",
                    title: "Close",
                    "aria-label": "Close",
                    class: "inline-flex h-6 w-6 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-surface-hover hover:text-gray-100",
                    onclick: onClose,
                }, closeIcon({class: "h-4 w-4"}))),
            div({class: "app-scroll min-h-0 flex-1 overflow-y-auto px-4 py-4"}, widget.node)),
    );
}
