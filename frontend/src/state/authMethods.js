import van from "vanjs-core";
import {capi} from "../capi/index.js";

/**
 * Which login methods the server currently accepts, for pages that render
 * before a session exists (login, first-time setup) and for the personal
 * password card. `status` is 'loading' | 'ready' | 'error'.
 */
export const authMethodsS = van.state({status: 'loading', passkeyLoginEnabled: true, passwordLoginEnabled: false, localCaAvailable: false, error: ''});

/**
 * Fetches the methods. The request is deferred to a microtask on purpose: the
 * API client reads the login state synchronously to build its auth header, and
 * a page constructed inside a reactive route binding would otherwise capture a
 * dependency on the login state and be rebuilt from scratch the moment a token
 * is stored, which is exactly what first-time setup does mid-flow.
 */
export function loadAuthMethods() {
    queueMicrotask(async () => {
        if (authMethodsS.val.status !== 'ready') {
            authMethodsS.val = {...authMethodsS.val, status: 'loading', error: ''};
        }
        try {
            const m = await capi.getV1AuthMethods();
            authMethodsS.val = {status: 'ready', passkeyLoginEnabled: !!m.passkeyLoginEnabled, passwordLoginEnabled: !!m.passwordLoginEnabled, localCaAvailable: !!m.localCaAvailable, error: ''};
        } catch (e) {
            authMethodsS.val = {status: 'error', passkeyLoginEnabled: true, passwordLoginEnabled: false, localCaAvailable: false, error: e?.message || 'request failed'};
        }
    });
}
