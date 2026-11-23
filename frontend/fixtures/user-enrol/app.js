// Enrol-or-recover fixture: the widget in both of its hosts, the login card
// behind the master password and the Users page dialog, over the users
// fixture's in-memory access API. Context and dataset persist in the hash.
import van from "vanjs-core";
import {capi} from "/src/capi/index.js";
import {loginS} from "/src/state/login.js";
import {authzGrantsS, authzTemplatesS, spacesS, usersMapS} from "/src/state/deployments.js";
import {usersPage} from "/src/pages/users.js";
import {closeExplainer} from "/src/components/ruleExplainer.js";
import {userEnrolOverlay} from "/src/components/userEnrol.js";
import {DATASETS, installMockApi, populate} from "/fixtures/users-redesign/mock.js";
import {shell} from "/fixtures/users-redesign/shell.js";
import {accountActions, enrolActions, resetAccounts, selfOf} from "./mock.js";
import {loginHost} from "./loginHost.js";

const {button, div, input, label, p, select, option, span} = van.tags;

const DEFAULTS = {context: "login", data: "typical", fail: "0", you: "1"};

// --- hash-persisted state ----------------------------------------------------

const fromHash = () => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const state = {...DEFAULTS};
    for (const key of Object.keys(DEFAULTS)) if (params.has(key)) state[key] = params.get(key);
    return state;
};

const initial = fromHash();
const context = van.state(initial.context);
const dataset = van.state(initial.data);
const fail = van.state(initial.fail === "1");
const you = van.state(initial.you);
const panelOpen = van.state(true);
const generation = van.state(0);

van.derive(() => {
    const params = new URLSearchParams();
    const current = {context: context.val, data: dataset.val, fail: fail.val ? "1" : "0", you: you.val};
    for (const [key, value] of Object.entries(current)) if (value !== DEFAULTS[key]) params.set(key, value);
    history.replaceState(null, "", `#${params.toString()}`);
});

// --- toasts ----------------------------------------------------------------------

const toasts = van.state([]);
const toast = (text) => {
    const entry = {text};
    toasts.val = [...toasts.val, entry];
    setTimeout(() => { toasts.val = toasts.val.filter((t) => t !== entry); }, 4000);
};
const toastLayer = div({class: "pointer-events-none fixed right-3 top-3 z-[90] flex flex-col gap-2"},
    () => div({class: "flex flex-col gap-2"}, ...toasts.val.map((t) => div(
        {class: "rounded-lg border border-emerald-900/60 bg-emerald-950/80 px-3 py-2 text-sm text-emerald-100 shadow-xl backdrop-blur"},
        t.text))));

// --- mock backend ------------------------------------------------------------

const author = () => Number(you.val) || 1;
installMockApi(capi, {author});
const account = {
    self: selfOf,
    actions: accountActions({scenario: () => ({failActions: fail.val}), author}),
    onManageSessions: () => toast("The app would open the Sessions page."),
};
const actions = enrolActions({
    scenario: () => ({failActions: fail.val}),
    author,
    onSignedIn: (user) => toast(`Signed in as ${user.name || `user ${user.id}`}. The app would now navigate to /.`),
});

const liveSpaces = () => (spacesS.val || []).filter((s) => s && !s.deleted);
const data = {
    users: () => [...usersMapS.val.values()],
    grants: () => authzGrantsS.val || [],
    templates: () => authzTemplatesS.val || [],
    spaces: liveSpaces,
    spaceNames: () => new Map(liveSpaces().map((s) => [Number(s.id), s.name || `space ${s.id}`])),
};

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const applyScenario = async () => {
    closeExplainer();
    const userId = author();
    loginS.val = {token: "fixture", userId, name: "", expiry: new Date(Date.now() + 864e5), scopes: []};
    await sleep(60);
    const data = populate(dataset.val);
    resetAccounts();
    if (!data.users.some((u) => u.id === userId)) you.val = String(data.users[0]?.id || 1);
    generation.val++;
};

// --- fixture chrome ----------------------------------------------------------

const chrome = div(
    {class: "fixed bottom-2 left-2 z-[80] w-[11rem] rounded-lg border border-gray-700 bg-gray-900/95 text-xs shadow-xl shadow-black/50 backdrop-blur"},
    div(
        {class: "flex items-center justify-between border-b border-gray-800 px-3 py-2"},
        span({class: "font-medium text-gray-200"}, "Fixture"),
        button({type: "button", class: "text-gray-400 hover:text-gray-100 cursor-pointer", onclick: () => { panelOpen.val = !panelOpen.val; }},
            () => panelOpen.val ? "Hide" : "Show"),
    ),
    () => !panelOpen.val ? "" : div(
        {class: "flex flex-col gap-2.5 px-2.5 py-2"},
        div({class: "flex flex-col gap-1.5"},
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Reached from"),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Host",
                () => select({class: "input w-24 py-0.5 text-xs", onchange: (e) => { context.val = e.target.value; generation.val++; }},
                    option({value: "login", selected: context.val === "login"}, "login"),
                    option({value: "users", selected: context.val === "users"}, "users page"))),
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Data"),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Set",
                () => select({class: "input w-24 py-0.5 text-xs", onchange: (e) => { dataset.val = e.target.value; applyScenario(); }},
                    ...Object.keys(DATASETS).map((key) => option({value: key, selected: key === dataset.val}, key)))),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "You",
                () => select({class: "input w-24 py-0.5 text-xs", onchange: (e) => { you.val = e.target.value; applyScenario(); }},
                    ...[...usersMapS.val.entries()].map(([id, user]) => option({value: String(id), selected: String(id) === you.val}, user.name || `user ${id}`)))),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Every action fails",
                input({type: "checkbox", checked: () => fail.val, onchange: (e) => { fail.val = e.target.checked; }})),
            button({type: "button", class: "self-start text-gray-500 hover:text-gray-200 cursor-pointer", onclick: applyScenario}, "Reset data")),
        p({class: "text-[10px] leading-snug text-gray-600"}, "Edits stay in memory; Reset data starts over."),
    ),
);

// --- mount --------------------------------------------------------------------

van.add(document.body,
    () => {
        generation.val;
        if (context.val === "login") {
            return loginHost({
                actions,
                data,
                onSignedIn: () => { generation.val++; },
                onBackToLogin: () => { toast("Back to the login page."); generation.val++; },
            });
        }
        return shell(usersPage({enrol: ({onClose}) => userEnrolOverlay({onClose, actions, ...data}), account}));
    },
    toastLayer,
    chrome,
);

applyScenario();
