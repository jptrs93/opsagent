// The enrol-or-recover widget's server seam over the real API. The same
// actions serve both hosts: on the login page the caller holds a root
// session, on the Users page an ordinary one with the right grants.
import {capi} from "../capi/index.js";
import {loginS, setLoginFromResponse} from "./login.js";
import {authzGrantsS, authzTemplatesS, spacesS, usersMapS} from "./deployments.js";
import {USER, written} from "./tree.js";
import {isRootSession} from "../lib/userSessions.js";

export const linkUrl = (token) => `${window.location.origin}/enrol/${token}`;

const createdUser = (update) => {
    const w = written(update, USER);
    if (!w) throw new Error("The server did not return the new user.");
    return {id: w.id, name: w.entity?.name || ""};
};

export const enrolActions = {
    async verifyMasterPassword(password) {
        const response = await capi.postV1AuthMaster({password});
        setLoginFromResponse(response);
    },
    async createUser({name, password, grants}) {
        const update = await capi.postV1UsersCreate({
            name: name?.trim() || undefined,
            password: password || undefined,
            grants: grants || [],
        });
        return createdUser(update);
    },
    async createLink({userId}) {
        const link = await capi.postV1UsersLink({userId: Number(userId)});
        return {url: linkUrl(link.token), expiresAt: link.expiresAt instanceof Date ? link.expiresAt.getTime() : Date.now() + 7 * 864e5};
    },
    async signIn({userId}) {
        if (!isRootSession(loginS.val)) throw new Error("Direct sign-in needs the master password.");
        const response = await capi.postV1AuthOpenSession({userId: Number(userId)});
        setLoginFromResponse(response);
    },
};

const liveSpaces = () => (spacesS.val || []).filter((s) => s && !s.deleted);

// The lists the widget reads, straight from the stream stores. A root
// session streams users, spaces, templates and grants and nothing else.
export const enrolData = {
    users: () => [...usersMapS.val.entries()].map(([id, user]) => ({
        id: Number(id),
        name: user?.name || "",
        createdAt: Number(user?.createdAt || 0),
        pending: !user?.activatedAt,
    })),
    grants: () => authzGrantsS.val || [],
    templates: () => authzTemplatesS.val || [],
    spaces: liveSpaces,
    spaceNames: () => new Map(liveSpaces().map((s) => [Number(s.id), s.name || `space ${s.id}`])),
};
