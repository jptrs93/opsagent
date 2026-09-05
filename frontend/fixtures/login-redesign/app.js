// Login page redesign fixture: renders the current page and three proposals
// against a mock auth backend, with scenario toggles in a floating panel.
// Selection and scenario persist in the URL hash so reloads keep them.
import van from "vanjs-core";
import {DEFAULT_SCENARIO, methodsFor, mockActions} from "./mock.js";
import {currentDesign} from "./designCurrent.js";
import {centeredDesign} from "./designCentered.js";
import {minimalDesign} from "./designMinimal.js";
import {consoleDesign} from "./designConsole.js";
import {cardDesign} from "./designCard.js";

const {button, div, input, label, p, select, option, span} = van.tags;

const DESIGNS = [
    {key: "current", label: "Current", render: currentDesign},
    {key: "centered", label: "A · Centered", render: centeredDesign},
    {key: "minimal", label: "B · Minimal", render: minimalDesign},
    {key: "console", label: "C · Console", render: consoleDesign},
    {key: "card", label: "D · Card", render: cardDesign},
];

// --- hash-persisted state ----------------------------------------------------

const fromHash = () => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const scenario = {...DEFAULT_SCENARIO};
    for (const key of Object.keys(DEFAULT_SCENARIO)) {
        if (!params.has(key)) continue;
        const raw = params.get(key);
        scenario[key] = typeof DEFAULT_SCENARIO[key] === "boolean" ? raw === "1" : raw;
    }
    return {design: params.get("design") || "centered", scenario};
};

const initial = fromHash();
const designKey = van.state(initial.design);
const scenario = van.state(initial.scenario);
// Collapsed to start on narrow viewports, where it would sit over the form.
const panelOpen = van.state(window.innerWidth >= 900);
const toast = van.state("");

van.derive(() => {
    const params = new URLSearchParams();
    params.set("design", designKey.val);
    for (const [key, value] of Object.entries(scenario.val)) {
        if (value === DEFAULT_SCENARIO[key]) continue;
        params.set(key, typeof value === "boolean" ? (value ? "1" : "0") : value);
    }
    history.replaceState(null, "", `#${params.toString()}`);
});

const setScenario = (patch) => { scenario.val = {...scenario.val, ...patch}; };

let toastTimer = 0;
const actions = mockActions(() => scenario.val, {
    onSignedIn: (name) => {
        toast.val = `Signed in as ${name}. The real page now navigates to /.`;
        clearTimeout(toastTimer);
        toastTimer = setTimeout(() => { toast.val = ""; }, 3000);
    },
    onRetryMethods: () => setScenario({methods: "ready"}),
});

// --- fixture chrome ----------------------------------------------------------

const check = (key, text) => label(
    {class: "flex cursor-pointer items-center gap-2 text-gray-300 hover:text-gray-100"},
    input({type: "checkbox", class: "accent-blue-500", checked: () => !!scenario.val[key], onchange: (e) => setScenario({[key]: e.target.checked})}),
    text,
);

const radio = (d) => label(
    {class: () => `flex cursor-pointer items-center gap-2 rounded px-1.5 py-0.5 ${designKey.val === d.key ? "bg-gray-800 text-white" : "text-gray-300 hover:text-gray-100"}`},
    input({type: "radio", name: "design", class: "accent-blue-500", checked: () => designKey.val === d.key, onchange: () => { designKey.val = d.key; }}),
    d.label,
);

const chrome = div(
    {class: "fixed bottom-3 left-3 z-50 w-56 rounded-lg border border-gray-700 bg-gray-900/95 text-xs shadow-xl shadow-black/50 backdrop-blur"},
    div(
        {class: "flex items-center justify-between border-b border-gray-800 px-3 py-2"},
        span({class: "font-medium text-gray-200"}, "Login fixture"),
        button({type: "button", class: "text-gray-400 hover:text-gray-100 cursor-pointer", onclick: () => { panelOpen.val = !panelOpen.val; }},
            () => panelOpen.val ? "Hide" : "Show"),
    ),
    () => !panelOpen.val ? "" : div(
        {class: "flex flex-col gap-3 px-3 py-2.5"},
        div({class: "flex flex-col gap-0.5"}, p({class: "mb-1 text-[10px] uppercase tracking-wide text-gray-500"}, "Design"), ...DESIGNS.map(radio)),
        div(
            {class: "flex flex-col gap-1.5"},
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Server"),
            check("passwordLogin", "Password login enabled"),
            check("passkeysOnServer", "Passkey login enabled"),
            check("localCa", "Local CA in use"),
            label({class: "flex items-center justify-between gap-2 text-gray-300"}, "Methods discovery",
                select({class: "input py-0.5 text-xs", value: () => scenario.val.methods, onchange: (e) => setScenario({methods: e.target.value})},
                    option({value: "ready"}, "ready"), option({value: "loading"}, "loading"), option({value: "error"}, "error"))),
        ),
        div(
            {class: "flex flex-col gap-1.5"},
            p({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "Client"),
            check("browserPasskeys", "Browser supports passkeys"),
            check("failSignIn", "Every sign-in fails"),
        ),
        button({type: "button", class: "self-start text-gray-500 hover:text-gray-200 cursor-pointer", onclick: () => { scenario.val = {...DEFAULT_SCENARIO}; }}, "Reset scenario"),
    ),
);

const toastEl = () => toast.val
    ? div({class: "fixed left-1/2 top-4 z-50 -translate-x-1/2 rounded-md border border-emerald-800 bg-emerald-950/90 px-3 py-1.5 text-xs text-emerald-200 shadow-lg"}, toast.val)
    : "";

// --- mount --------------------------------------------------------------------

// The page is rebuilt whenever the design or scenario changes, so every
// design sees a plain methods object and fresh internal state.
van.add(document.body,
    () => {
        const design = DESIGNS.find((d) => d.key === designKey.val) || DESIGNS[0];
        return design.render({methods: methodsFor(scenario.val), actions});
    },
    toastEl,
    chrome,
);
