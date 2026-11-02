// Nodes fixture: the shipped page (src/pages/cluster.js) inside a static
// dashboard shell, over an in-memory node API. The floating panel switches the dataset;
// it persists in the URL hash.
import van from "vanjs-core";
import {capi} from "/src/capi/index.js";
import {loginS} from "/src/state/login.js";
import {DATASETS, installMockApi, populate} from "./mock.js";
import {clusterPage} from "/src/pages/cluster.js";
import {shell} from "./shell.js";

const {button, div, label, p, select, option, span} = van.tags;

const DEFAULTS = {data: "typical"};

const fromHash = () => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const state = {...DEFAULTS};
    for (const key of Object.keys(DEFAULTS)) if (params.has(key)) state[key] = params.get(key);
    return state;
};

const initial = fromHash();
const dataset = van.state(initial.data);
const panelOpen = van.state(window.innerWidth >= 900);
const generation = van.state(0);

van.derive(() => {
    const params = new URLSearchParams();
    if (dataset.val !== DEFAULTS.data) params.set("data", dataset.val);
    history.replaceState(null, "", `#${params.toString()}`);
});

installMockApi(capi);

// Setting the login state triggers the stream module's derive, which resets
// every store a tick later; the data goes in after that has happened.
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const applyScenario = async () => {
    loginS.val = {token: "fixture", userId: 1, name: "", expiry: new Date(Date.now() + 864e5), scopes: []};
    await sleep(60);
    populate(dataset.val);
    generation.val++;
};

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
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Data"),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Set",
                select({class: "input w-24 py-0.5 text-xs", value: () => dataset.val, onchange: (e) => { dataset.val = e.target.value; applyScenario(); }},
                    ...Object.keys(DATASETS).map((key) => option({value: key, selected: () => key === dataset.val}, key)))),
            button({type: "button", class: "self-start text-gray-500 hover:text-gray-200 cursor-pointer", onclick: applyScenario}, "Reset data")),
        p({class: "text-[10px] leading-snug text-gray-600"},
            "Edits stay in memory; Reset data starts over."),
    ),
);

van.add(document.body,
    () => {
        generation.val;
        return shell(clusterPage());
    },
    chrome,
);

applyScenario();
