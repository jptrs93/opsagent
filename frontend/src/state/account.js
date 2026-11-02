// The signed-in user's own account: what /v1/account reports, plus the
// self-service actions the You tab offers. Passkey ids come as bytes and are
// kept both raw (for delete) and as a base64url string (for keys and testids).
import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS, setLoginFromResponse} from "./login.js";
import {usersMapS} from "./deployments.js";
import {credentialToJSONBytes, registrationOptionsFromJSONBytes} from "../util/webauthn.js";

export const accountS = van.state(null);

const b64url = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");

const toMillis = (value) => value instanceof Date && value.getTime() > 0 ? value.getTime() : null;

export async function loadAccount() {
    const view = await capi.getV1Account();
    accountS.val = {
        id: Number(view.userId || 0),
        name: view.name || "",
        email: view.email || "",
        hasPassword: Boolean(view.hasPassword),
        activatedAt: toMillis(view.activatedAt),
        passkeys: (view.passkeys || []).map((p) => ({id: b64url(p.id), rawId: p.id, label: p.label || "", createdAt: toMillis(p.createdAt) || 0, lastUsedAt: toMillis(p.lastUsedAt)})),
    };
    return accountS.val;
}

// self() merges the account view with the user entity from the stream, so
// the panel has the joined date and the pending state without a second
// request.
export function accountSelf(userId) {
    const id = Number(userId || loginS.val?.userId || 0);
    const entity = usersMapS.val.get(id);
    const view = accountS.val && accountS.val.id === id ? accountS.val : null;
    if (!entity && !view) return null;
    return {
        id,
        name: view?.name ?? entity?.name ?? "",
        createdAt: Number(entity?.createdAt || 0),
        pending: view ? !view.activatedAt : !entity?.activatedAt,
        hasPassword: Boolean(view?.hasPassword),
        passkeys: view?.passkeys || [],
    };
}

export async function registerPasskey() {
    const startResponse = await capi.postV1AuthPasskeyRegisterStart();
    const credential = await navigator.credentials.create(registrationOptionsFromJSONBytes(startResponse.optionsJson));
    if (!credential) throw new Error("Passkey registration returned no credential.");
    const response = await capi.postV1AuthPasskeyRegisterFinish({sessionId: startResponse.sessionId, credentialJson: credentialToJSONBytes(credential)});
    if (response?.token && response.token !== loginS.val?.token) setLoginFromResponse(response);
}

export const accountActions = {
    async rename({name}) {
        await capi.postV1AccountRename({name});
        await loadAccount();
    },
    async changePassword({current, next}) {
        await capi.postV1AccountPassword({current: current || undefined, next});
        await loadAccount();
    },
    async addPasskey() {
        await registerPasskey();
        await loadAccount();
    },
    async removePasskey({id}) {
        const passkey = (accountS.val?.passkeys || []).find((p) => p.id === id);
        if (!passkey) throw new Error("Passkey not found");
        await capi.postV1AccountPasskeysDelete({id: passkey.rawId});
        await loadAccount();
    },
};
