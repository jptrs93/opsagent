// The enrol widget's server seam, in memory: users land in usersMapS with a
// pending flag and their grants in authzGrantsS, so the real Users page shows
// them. Links are random tokens under /enrol/.
import van from "vanjs-core";
import {authzGrantsS, usersMapS} from "/src/state/deployments.js";
import {ENROL_LINK_DAYS} from "/src/components/userEnrol.js";

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const token = () => {
    const bytes = new Uint8Array(24);
    crypto.getRandomValues(bytes);
    return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
};

const nextId = (ids) => ids.reduce((max, id) => Math.max(max, Number(id) || 0), 0) + 1;

export function enrolActions({delayMs = 600, scenario = () => ({}), onSignedIn, author = () => 0}) {
    return {
        async verifyMasterPassword(password) {
            await delay(delayMs);
            if (!password || scenario().failActions) throw new Error("Invalid master password");
        },
        async createUser({name, password, grants}) {
            await delay(delayMs);
            if (scenario().failActions) throw new Error("500 internal: the write log is read only");
            const trimmed = (name || "").trim();
            if (trimmed && [...usersMapS.val.values()].some((u) => (u.name || "").trim() === trimmed)) {
                throw new Error(`A user named ${trimmed} already exists`);
            }
            const id = nextId([...usersMapS.val.keys()]);
            const user = {id, name: trimmed, createdAt: Date.now(), activatedAt: undefined, hasPassword: Boolean(password)};
            usersMapS.val = new Map([...usersMapS.val, [id, user]]);
            let grantId = nextId(authzGrantsS.val.map((g) => g.id));
            authzGrantsS.val = [
                ...authzGrantsS.val,
                ...(grants || []).map((grant) => ({id: grantId++, userId: id, author: author(), createdAt: Date.now(), grant})),
            ];
            return user;
        },
        async createLink({userId}) {
            await delay(delayMs);
            if (scenario().failActions) throw new Error("500 internal: the write log is read only");
            if (!usersMapS.val.has(Number(userId))) throw new Error("No such user");
            return {url: `${window.location.origin}/enrol/${token()}`, expiresAt: Date.now() + ENROL_LINK_DAYS * 864e5};
        },
        async signIn({userId}) {
            await delay(delayMs);
            if (scenario().failActions) throw new Error("Sign-in failed");
            const user = usersMapS.val.get(Number(userId));
            if (!user) throw new Error("No such user");
            usersMapS.val = new Map([...usersMapS.val, [user.id, {...user, activatedAt: new Date()}]]);
            onSignedIn?.(user);
        },
    };
}

const day = (iso) => new Date(iso).getTime();
const hoursAgo = (h) => Date.now() - h * 3600e3;

// Per-user sign-in state the users fixture's datasets do not carry.
export const accountS = van.state(new Map());
export const resetAccounts = () => {
    accountS.val = new Map([
        [1, {hasPassword: true, passkeys: [
            {id: "pk-1", label: "MacBook Pro · Touch ID", createdAt: day("2026-03-02T09:05:00Z"), lastUsedAt: hoursAgo(1)},
            {id: "pk-2", label: "YubiKey 5C", createdAt: day("2026-03-10T14:00:00Z"), lastUsedAt: day("2026-09-28T08:12:00Z")},
        ]}],
        [2, {hasPassword: false, passkeys: [{id: "pk-3", label: "iPhone · Face ID", createdAt: day("2026-04-11T13:35:00Z"), lastUsedAt: hoursAgo(26)}]}],
        [3, {hasPassword: true, passkeys: []}],
    ]);
};
resetAccounts();

export const selfOf = (id) => {
    const user = usersMapS.val.get(Number(id));
    if (!user) return null;
    const extra = accountS.val.get(Number(id)) || {hasPassword: Boolean(user.hasPassword), passkeys: []};
    return {...user, ...extra};
};

const setAccount = (id, patch) => {
    const current = accountS.val.get(Number(id)) || {hasPassword: false, passkeys: []};
    accountS.val = new Map([...accountS.val, [Number(id), {...current, ...patch}]]);
};

export function accountActions({delayMs = 600, scenario = () => ({}), author = () => 1}) {
    const fail = () => { if (scenario().failActions) throw new Error("500 internal: the write log is read only"); };
    return {
        async rename({name}) {
            await delay(delayMs);
            fail();
            const trimmed = (name || "").trim();
            if (!trimmed) throw new Error("A username is required");
            const id = Number(author());
            if ([...usersMapS.val.values()].some((u) => u.id !== id && (u.name || "").trim() === trimmed)) {
                throw new Error(`A user named ${trimmed} already exists`);
            }
            const user = usersMapS.val.get(id);
            usersMapS.val = new Map([...usersMapS.val, [id, {...user, name: trimmed}]]);
        },
        async changePassword({current, next}) {
            await delay(delayMs);
            fail();
            const me = selfOf(author());
            if (me.hasPassword && current === "wrong") throw new Error("Current password did not match");
            if (!next) throw new Error("A new password is required");
            setAccount(author(), {hasPassword: true});
        },
        async addPasskey() {
            await delay(delayMs);
            fail();
            const me = selfOf(author());
            const passkey = {id: `pk-${token().slice(0, 6)}`, label: "Passkey on this device", createdAt: Date.now(), lastUsedAt: null};
            setAccount(author(), {passkeys: [...me.passkeys, passkey]});
            return passkey;
        },
        async removePasskey({id}) {
            await delay(delayMs);
            fail();
            const me = selfOf(author());
            if (me.passkeys.length <= 1 && !me.hasPassword) throw new Error("This is your only way to sign in");
            setAccount(author(), {passkeys: me.passkeys.filter((p) => p.id !== id)});
        },
    };
}
