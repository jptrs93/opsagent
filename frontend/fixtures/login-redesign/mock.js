// Mock server for the login fixture: the auth-methods discovery response and
// the two sign-in calls, driven by the scenario toggles in the fixture chrome.

export const DEFAULT_SCENARIO = {
    passwordLogin: true,     // auth.password_login_enabled on the server
    passkeysOnServer: true,  // passkey login enabled on the server
    browserPasskeys: true,   // navigator.credentials available in this browser
    localCa: true,           // server is serving under the locally generated CA
    methods: "ready",        // "ready" | "loading" | "error" — discovery state
    failSignIn: false,       // every sign-in attempt fails
};

// methodsFor mirrors the shape of authMethodsS in src/state/authMethods.js.
export function methodsFor(s) {
    if (s.methods === "loading") {
        return {status: "loading", passkeyLoginEnabled: true, passwordLoginEnabled: false, localCaAvailable: false, error: ""};
    }
    if (s.methods === "error") {
        return {status: "error", passkeyLoginEnabled: true, passwordLoginEnabled: false, localCaAvailable: false, error: "fetch failed: 502 Bad Gateway"};
    }
    return {
        status: "ready",
        passkeyLoginEnabled: s.passkeysOnServer,
        passwordLoginEnabled: s.passwordLogin,
        localCaAvailable: s.localCa,
        error: "",
    };
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const FAKE_PEM = `-----BEGIN CERTIFICATE-----
MIIBkTCCATegAwIBAgIRAJ7f0bK3fixture0000000wCgYIKoZIzj0EAwIwGjEYMBYG
A1UEAxMPT3BlbkRlcGxveSBMb2NhbCBDQTAeFw0yNjA5MDEwMDAwMDBaFw0zNjA4
-----END CERTIFICATE-----
`;

// mockActions is the seam every design renders against; the real page would
// bind these to capi + navigator.credentials (see src/pages/login.js).
export function mockActions(scenario, {onSignedIn, onRetryMethods}) {
    return {
        browserSupportsPasskeys: () => scenario().browserPasskeys,
        passkeyLogin: async () => {
            await delay(1200);
            if (scenario().failSignIn) {
                const e = new Error("The operation either timed out or was not allowed.");
                e.name = "NotAllowedError";
                throw e;
            }
            onSignedIn("joss");
        },
        passwordLogin: async (username, password) => {
            await delay(1200);
            if (scenario().failSignIn || !password) throw new Error("bad credentials");
            onSignedIn(username);
        },
        retryMethods: () => onRetryMethods(),
        fetchCA: async () => { await delay(150); return FAKE_PEM; },
        caURL: `${window.location.origin}/v1/tls/ca.crt`,
        bootstrapHref: "/bootstrap",
    };
}
