// Users & roles fixture: the shipped page (src/pages/users.js) inside a
// static dashboard shell, over an in-memory access API. The floating panel
// switches the dataset and the signed-in user; both persist in the URL hash.
import van from "vanjs-core";
import {capi} from "/src/capi/index.js";
import {loginS} from "/src/state/login.js";
import {usersMapS} from "/src/state/deployments.js";
import {DATASETS, installMockApi, populate} from "./mock.js";
import {usersPage} from "/src/pages/users.js";
import {closeExplainer} from "/src/components/ruleExplainer.js";
import {shell} from "./shell.js";

const {button, div, label, p, select, option, span} = van.tags;

const DEFAULTS = {data: "typical", you: "1"};

// --- hash-persisted state ----------------------------------------------------

const fromHash = () => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const state = {...DEFAULTS};
    for (const key of Object.keys(DEFAULTS)) if (params.has(key)) state[key] = params.get(key);
    return state;
};

const initial = fromHash();
const dataset = van.state(initial.data);
const you = van.state(initial.you);
const panelOpen = van.state(window.innerWidth >= 900);
// generation bumps whenever the page must be rebuilt from fresh data.
const generation = van.state(0);

van.derive(() => {
    const params = new URLSearchParams();
    const current = {data: dataset.val, you: you.val};
    for (const [key, value] of Object.entries(current)) if (value !== DEFAULTS[key]) params.set(key, value);
    history.replaceState(null, "", `#${params.toString()}`);
});

// --- mock backend ------------------------------------------------------------

installMockApi(capi, {author: () => Number(you.val) || 1});

// Setting the login state triggers the stream module's derive, which resets
// every store a tick later; the data goes in after that has happened.
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const applyScenario = async () => {
    closeExplainer();
    const userId = Number(you.val) || 1;
    loginS.val = {token: "fixture", userId, name: "", expiry: new Date(Date.now() + 864e5), scopes: []};
    await sleep(60);
    const data = populate(dataset.val);
    if (!data.users.some((u) => u.id === userId)) you.val = String(data.users[0]?.id || 1);
    generation.val++;
};

// --- fixture chrome ----------------------------------------------------------

const chrome = div(
    // Narrow enough to sit inside the sidebar column, so it never covers the
    // page under review.
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
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Data"),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Set",
                select({class: "input w-24 py-0.5 text-xs", value: () => dataset.val, onchange: (e) => { dataset.val = e.target.value; applyScenario(); }},
                    ...Object.keys(DATASETS).map((key) => option({value: key}, key)))),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "You",
                () => select({class: "input w-24 py-0.5 text-xs", onchange: (e) => { you.val = e.target.value; applyScenario(); }},
                    ...[...usersMapS.val.entries()].map(([id, user]) => option({value: String(id), selected: String(id) === you.val}, user.name || `user ${id}`)))),
            button({type: "button", class: "self-start text-gray-500 hover:text-gray-200 cursor-pointer", onclick: applyScenario}, "Reset data")),
        p({class: "text-[10px] leading-snug text-gray-600"},
            "Edits stay in memory; Reset data starts over."),
    ),
);

// --- mount --------------------------------------------------------------------

// The page is rebuilt on every generation so a dataset or user change starts
// it from clean state, as a navigation would.
van.add(document.body,
    () => {
        generation.val;
        return shell(usersPage());
    },
    chrome,
);

applyScenario();
