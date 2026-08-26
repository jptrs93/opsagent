import van from "vanjs-core";
import {checkIcon, chevronDownIcon} from "../lib/icons.js";

const {button, div, span} = van.tags;

// logScopePicker scopes a log search to a deployment config version, instance
// ordinal, and run. scopeS holds {version, instance, run}: version/run 0 and
// instance null mean "all". The instance column disappears when the deployment
// has a single instance, and run selection only opens up once the scope pins a
// specific version and instance (the single instance counts as pinned) — runs
// are only well-defined within one.
//
// versionsS is null until ensureVersions() resolves; fetchRuns(version,
// instance) resolves {runs, distinct} for the current search range. Both are
// invoked lazily when the panel opens or the scope changes.
export function logScopePicker({scopeS, ordinalsS, versionsS, ensureVersions, fetchRuns, disabledS, onChange}) {
    const open = van.state(false);
    const runsState = van.state({loading: false, runs: [], distinct: 0, error: ''});
    let runsKey = null;

    const multiInstance = () => (ordinalsS.val || []).length > 1;
    const runEligible = () => Boolean(scopeS.val.version) && (!multiInstance() || scopeS.val.instance != null);

    const label = () => {
        const s = scopeS.val;
        const parts = [s.version ? `Version ${s.version}` : 'All versions'];
        if (multiInstance()) {
            parts.push(s.instance != null ? `instance ${s.instance}` : (s.version ? 'all instances' : 'instances'));
        }
        if (s.run) parts.push(`run ${s.run}`);
        return parts.join(' & ');
    };

    const loadRuns = async () => {
        if (!runEligible()) {
            runsKey = null;
            runsState.val = {loading: false, runs: [], distinct: 0, error: ''};
            return;
        }
        const instance = multiInstance() ? scopeS.val.instance : null;
        const key = `${scopeS.val.version}|${instance}`;
        if (runsKey === key && !runsState.val.error) return;
        runsKey = key;
        runsState.val = {loading: true, runs: [], distinct: 0, error: ''};
        try {
            const {runs, distinct} = await fetchRuns(scopeS.val.version, instance);
            if (runsKey !== key) return;
            runsState.val = {loading: false, runs, distinct, error: ''};
        } catch (e) {
            if (runsKey !== key) return;
            runsState.val = {loading: false, runs: [], distinct: 0, error: e.message || String(e)};
        }
    };

    const setScope = (patch) => {
        scopeS.val = {...scopeS.val, ...patch};
        void loadRuns();
        onChange?.();
    };

    const item = (selected, text, onclick) => button({
        type: "button",
        class: "flex w-full cursor-pointer items-center gap-1.5 px-2 py-1 text-left text-xs text-gray-200 hover:bg-gray-800",
        onclick,
    }, span({class: "w-3.5 flex-none"}, selected ? checkIcon({class: "w-3.5 h-3.5 text-brand"}) : ''), text);

    const note = (text) => div({class: "px-2.5 py-1 text-[11px] text-gray-600"}, text);

    const column = (title, testid, body) => div(
        {"data-testid": testid, class: "flex w-40 flex-none flex-col border-r border-gray-800 last:border-r-0"},
        div({class: "px-2.5 pb-0.5 pt-2 text-[10px] font-medium uppercase tracking-wide text-gray-500"}, title),
        div({class: "app-scroll max-h-64 min-h-24 flex-1 overflow-y-auto pb-1"}, body),
    );

    const versionColumn = () => column("version", "logs-scope-versions", (() => {
        const versions = versionsS.val;
        if (versions === null) return note("Loading…");
        return div(
            item(!scopeS.val.version, "All versions", () => setScope({version: 0, run: 0})),
            ...versions.map(v => item(scopeS.val.version === v, `Version ${v}`, () => setScope({version: v, run: 0}))),
        );
    })());

    const instanceColumn = () => column("instance", "logs-scope-instances", div(
        item(scopeS.val.instance == null, "All instances", () => setScope({instance: null, run: 0})),
        ...(ordinalsS.val || []).map(n => item(scopeS.val.instance === n, `Instance ${n}`, () => setScope({instance: n, run: 0}))),
    ));

    const runColumn = () => column("run", "logs-scope-runs", (() => {
        if (!runEligible()) {
            return note(multiInstance()
                ? "Select a version and instance to filter by run."
                : "Select a version to filter by run.");
        }
        const rs = runsState.val;
        if (rs.loading) return note("Loading…");
        if (rs.error) return note(`Failed loading runs: ${rs.error}`);
        if (rs.runs.length === 0) return note("No runs in the current time range.");
        return div(
            item(!scopeS.val.run, "All runs", () => setScope({run: 0})),
            ...rs.runs.map(r => item(scopeS.val.run === r, `Run ${r}`, () => setScope({run: r}))),
            rs.distinct > rs.runs.length ? note(`+ ${rs.distinct - rs.runs.length} more not listed`) : '',
        );
    })());

    return div(
        {class: "relative"},
        button({
            "data-testid": "logs-scope-button",
            type: "button",
            disabled: () => Boolean(disabledS && disabledS.val),
            class: "input flex items-center gap-1.5 whitespace-nowrap py-1 text-xs text-gray-200 cursor-pointer hover:bg-gray-700 disabled:cursor-default disabled:opacity-50 disabled:hover:bg-gray-800",
            onclick: () => {
                open.val = !open.val;
                if (open.val) {
                    void ensureVersions?.();
                    void loadRuns();
                }
            },
        }, () => label(), chevronDownIcon({class: "w-3 h-3 text-gray-500"})),
        () => !open.val ? '' : div(
            div({class: "fixed inset-0 z-20", onclick: () => { open.val = false; }}),
            div(
                {"data-testid": "logs-scope-panel", class: "absolute left-0 top-full z-30 mt-1 flex rounded border border-gray-700 bg-gray-900 shadow-xl"},
                () => versionColumn(),
                () => multiInstance() ? instanceColumn() : '',
                () => runColumn(),
            ),
        ),
    );
}
