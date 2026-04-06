import van from "vanjs-core";
import {navigate} from "../lib/router.js";

/**
 * @typedef {Object} LoginState
 * @property {string} token - The original JWT token string
 * @property {number} userId - The authenticated user ID
 * @property {string} name - The authenticated user name
 * @property {Date} expiry - The expiration date of the token
 * @property {string[]} scopes - The user's scopes
 */

/** @type {State<LoginState|null>} */
export const loginS = van.state(null)

const logoutHandlers = new Set()
let logoutTimer = null

const clearLogoutTimer = () => {
    if (logoutTimer) {
        clearTimeout(logoutTimer)
        logoutTimer = null
    }
}

const normalizeLoginResponse = (response) => {
    const expiry = response?.expiry instanceof Date ? response.expiry : new Date(response?.expiry)
    if (!response?.token || Number.isNaN(expiry.getTime())) {
        throw new Error('invalid login response')
    }
    return {
        token: response.token,
        userId: response.userId || 0,
        name: response.name || '',
        expiry,
        scopes: Array.isArray(response.scopes) ? response.scopes : [],
    }
}

const setLoginState = (nextState) => {
    clearLogoutTimer()
    if (!nextState) {
        loginS.val = null
        return
    }

    const delay = nextState.expiry.getTime() - Date.now()
    if (delay <= 0) {
        clearLoginState()
        navigate("/login", {replace: true})
        return
    }

    loginS.val = nextState
    logoutTimer = setTimeout(() => {
        clearLoginState()
        navigate("/login", {replace: true})
    }, delay)
}

export async function initLoginState() {
    const session = localStorage.getItem('authSession');
    if (session) {
        try {
            const parsedSession = normalizeLoginResponse(JSON.parse(session));
            if (parsedSession.expiry < new Date()) {
                console.log(`token expired ${parsedSession.expiry}`);
                clearLoginState();
            } else {
                setLoginState(parsedSession);
            }
        } catch (e) {
            console.log(`error parsing persisted auth session: ${e}`);
            clearLoginState();
        }
        return;
    }

    const legacyToken = localStorage.getItem('authToken');
    if (legacyToken) {
        setLoginState({
            token: legacyToken,
            userId: 0,
            name: '',
            expiry: new Date(Date.now() + 5 * 60 * 1000),
            scopes: [],
        })
    }
}

export function onLogout(handler) {
    logoutHandlers.add(handler)
    return () => logoutHandlers.delete(handler)
}

export function clearLoginState() {
    clearLogoutTimer()
    for (const handler of [...logoutHandlers]) {
        try {
            handler()
        } catch (e) {
            console.error(`error running logout handler: ${e}`)
        }
    }
    localStorage.removeItem('authSession')
    localStorage.removeItem('authToken')
    loginS.val = null
}

/**
 * Sets login state from a LoginResponse and persists to localStorage.
 * @param {import("../capi/model.js").LoginResponse} response
 */
export function setLoginFromResponse(response) {
    const nextState = normalizeLoginResponse(response)
    setLoginState(nextState)
    localStorage.setItem('authSession', JSON.stringify({
        token: nextState.token,
        userId: nextState.userId,
        name: nextState.name,
        expiry: nextState.expiry.toISOString(),
        scopes: nextState.scopes,
    }))
    localStorage.removeItem('authToken')
}
