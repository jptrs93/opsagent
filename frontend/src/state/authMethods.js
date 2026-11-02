import van from "vanjs-core";
import {capi} from "../capi/index.js";

/**
 * Which login methods the server currently accepts, for pages that render
 * before a session exists (login, enrol, link landing) and for the account
 * panel. `status` is 'loading' | 'ready' | 'error'. `requirePasskey` means
 * password sign-in is turned off.
 */
export const authMethodsS = van.state({status: 'loading', passkeyLoginEnabled: true, requirePasskey: false, localCaAvailable: false, error: ''});

const FRESH_MS = 60_000;
let loadedAt = 0;

/**
 * Fetches the methods. The request is deferred to a microtask on purpose: the
 * API client reads the login state synchronously to build its auth header, and
 * a page constructed inside a reactive route binding would otherwise capture a
 * dependency on the login state and be rebuilt from scratch the moment a token
 * is stored, which is exactly what the enrol page does mid-flow.
 */
export function loadAuthMethods() {
    queueMicrotask(async () => {
        // rawVal: a read of `.val` here would register the methods state as a
        // dependency of whichever page binding called us, and the 'loading'
        // write below would then rebuild that page, which calls us again.
        const current = authMethodsS.rawVal;
        if (current.status === 'ready' && Date.now() - loadedAt < FRESH_MS) return;
        if (current.status !== 'loading') {
            authMethodsS.val = {...current, status: 'loading', error: ''};
        }
        try {
            const m = await capi.getV1AuthMethods();
            loadedAt = Date.now();
            authMethodsS.val = {status: 'ready', passkeyLoginEnabled: !!m.passkeyLoginEnabled, requirePasskey: !!m.requirePasskey, localCaAvailable: !!m.localCaAvailable, error: ''};
        } catch (e) {
            authMethodsS.val = {status: 'error', passkeyLoginEnabled: true, requirePasskey: false, localCaAvailable: false, error: e?.message || 'request failed'};
        }
    });
}
