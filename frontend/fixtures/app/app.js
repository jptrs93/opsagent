import van from "vanjs-core";
import "/src/state/theme.js";
import {capi} from "/src/capi/index.js";
import "/src/state/deployments.js";
import {loginS} from "/src/state/login.js";
import {currentPath} from "/src/lib/router.js";
import {isRootSession} from "/src/lib/userSessions.js";
import {dashboard} from "/src/pages/dashboard.js";
import {loginPage} from "/src/pages/login.js";
import {enrolPage} from "/src/pages/enrol.js";
import {enrolLandingPage} from "/src/pages/enrolLanding.js";
import {CURRENT_SESSION_ID, SIGNED_IN_USER} from "./data.js";
import {installServer} from "./server.js";

const {a, button, div, span} = van.tags;

const params = new URLSearchParams(location.search);
const view = params.get("view") || "app";
const fixtureUrl = location.pathname + location.search + location.hash;

const {store, unmocked} = installServer(capi);
window.__fixture = {store, unmocked, view};

const defaults = {
    opsagent_status_hidden_spaces: "[]",
    opsagent_status_hidden_states: "[]",
    opendeploySecretsExplorerView: JSON.stringify({expanded: ["space:1", "space:2", "space:3", "space:4", "dir:1", "dir:2", "dir:3", "dir:5", "dir:7", "dir:8"]}),
    opendeployAssetsExplorerView: JSON.stringify({expanded: ["space:1", "space:2", "space:3", "space:4", "dir:1", "dir:3", "dir:4", "dir:5"]}),
};
for (const [key, value] of Object.entries(defaults)) {
    try { if (localStorage.getItem(key) == null) localStorage.setItem(key, value); } catch {}
}

// The pages navigate with the app router; the fixture keeps its own URL so a
// reload lands back here instead of on the real app.
van.derive(() => {
    currentPath.val;
    if (location.pathname + location.search + location.hash !== fixtureUrl) history.replaceState(null, "", fixtureUrl);
});

const signIn = () => {
    loginS.val = {token: "fixture-0-1", userId: SIGNED_IN_USER, name: "joss", expiry: new Date(Date.now() + 6 * 864e5), kind: 0, sessionId: CURRENT_SESSION_ID};
};

const views = {
    app: () => { signIn(); return () => loginS.val && !isRootSession(loginS.val) ? dashboard() : loginPage(); },
    login: () => () => loginS.val && !isRootSession(loginS.val) ? dashboard() : loginPage(),
    enrol: () => enrolPage(),
    landing: () => enrolLandingPage("l_fixture0e1f04b7d2a9c3"),
};

const chrome = () => {
    if (params.get("chrome") === "0") return "";
    const open = van.state(false);
    const link = (key) => a({href: `?view=${key}`, class: `px-2 py-0.5 rounded ${key === view ? "bg-white/15 text-white" : "text-gray-400 hover:text-gray-100"}`}, key);
    return div({class: "fixed top-1 right-1 z-[90] flex items-center gap-1 rounded border border-gray-700 bg-gray-900/90 px-1 py-0.5 text-[11px] shadow-lg"},
        () => open.val ? span({class: "flex items-center gap-0.5"}, ...Object.keys(views).map(link)) : "",
        button({type: "button", class: "px-1.5 text-gray-400 hover:text-gray-100 cursor-pointer", title: "Fixture views", onclick: () => { open.val = !open.val; }},
            () => open.val ? "×" : "fixture"));
};

van.add(document.body, (views[view] || views.app)(), chrome());
