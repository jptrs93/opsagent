import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {secretMetasS, secretsStatusS, userConfigsS} from "../state/deployments.js";

const { div, h2, p, span, input, button, table, thead, tbody, tr, th, td, code } = van.tags;
const { svg, path, circle, line } = van.tags("http://www.w3.org/2000/svg");

const svgBase = {
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round",
    class: "w-4 h-4",
};
const DEFAULT_SECRET_MASK = "••••••••••••••••";

const eyeOpenIcon = () => svg(svgBase,
    path({d: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"}),
    circle({cx: "12", cy: "12", r: "3"}));
const eyeOffIcon = () => svg(svgBase,
    path({d: "M9.9 4.24A9.1 9.1 0 0 1 12 4c7 0 10 8 10 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"}),
    path({d: "M6.61 6.61A18.5 18.5 0 0 0 2 12s3 8 10 8a9.1 9.1 0 0 0 5.39-1.61"}),
    line({x1: "2", y1: "2", x2: "22", y2: "22"}));
const trashIcon = () => svg(svgBase,
    path({d: "M3 6h18"}),
    path({d: "M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"}),
    path({d: "M10 11v6M14 11v6"}),
    path({d: "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}));
const plusIcon = () => svg(svgBase, line({x1: "12", y1: "5", x2: "12", y2: "19"}), line({x1: "5", y1: "12", x2: "19", y2: "12"}));

const rawStateValue = (state) => state.rawVal ?? state.val ?? "";

const iconButton = (child, onclick, cls = "") => button({
    type: "button",
    class: `p-1.5 rounded text-gray-400 hover:text-gray-100 hover:bg-surface-hover transition-colors cursor-pointer ${cls}`,
    onclick,
}, child);

const smallBtn = (text, onclick, cls, disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `text-xs px-3 py-1 rounded-md font-medium transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: async (e) => { if (disabledWhen && disabledWhen()) return; await onclick(e); },
}, text);

const actionButton = (text, onclick, cls = "bg-gray-700 text-gray-200 hover:bg-gray-600", disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `flex items-center gap-1 whitespace-nowrap text-sm px-3 py-1.5 rounded-lg transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: (e) => { if (disabledWhen && disabledWhen()) return; onclick(e); },
}, plusIcon(), text);

export function secretsPage() {
    const rows = van.state(null);
    const error = van.state(null);
    const search = van.state("");
    const sort = van.state({key: "name", dir: "asc"});
    let localRows = null;
    let streamSignature = '';

    const errorBanner = () => error.val ? p({class: "text-red-400 text-sm"}, `Error: ${error.val}`) : "";

    const makeConfigRow = (config) => {
        const isNew = !config;
        return {
            type: "config", isNew, _saved: false,
            name: van.state(config ? config.name : ""),
            value: van.state(config ? config.value : ""),
            orig: {
                name: config ? config.name : "",
                value: config ? config.value : "",
            },
        };
    };

    const makeSecretRow = (meta) => {
        const isNew = !meta;
        return {
            type: "secret", meta, isNew, _saved: false,
            name: van.state(meta ? meta.name : ""),
            value: van.state(""),
            revealed: van.state(false),
            loaded: van.state(isNew),
            valueDirty: van.state(false),
            orig: {name: meta ? meta.name : "", value: ""},
        };
    };

    const isDirty = (row) => row.type === "config"
        ? row.isNew || row.name.val !== row.orig.name || row.value.val !== row.orig.value
        : row.isNew || row.name.val !== row.orig.name || row.valueDirty.val;

    const setRows = (next) => {
        localRows = next;
        rows.val = next;
    };

    const rowKey = (row) => `${row.type}:${row.orig.name}`;

    const syncRowsFromUniverse = () => {
        const status = secretsStatusS.val;
        if (!status) return;
        const existing = new Map((localRows || [])
            .filter(row => !row.isNew && row.orig.name)
            .map(row => [rowKey(row), row]));
        const preserveOrMake = (key, make) => {
            const current = existing.get(key);
            return current && isDirty(current) ? current : make();
        };
        const secretRows = status.unlocked
            ? (secretMetasS.val || []).map(meta => preserveOrMake(`secret:${meta.name}`, () => makeSecretRow(meta)))
            : [];
        const configRows = (userConfigsS.val || []).map(config => preserveOrMake(`config:${config.name}`, () => makeConfigRow(config)));
        const pending = (localRows || []).filter(row => row.isNew && !row._saved);
        setRows([...secretRows, ...configRows, ...pending]);
    };

    van.derive(() => {
        const status = secretsStatusS.val;
        const signature = JSON.stringify({
            status,
            secrets: (secretMetasS.val || []).map(item => [item.id, item.name, item.group, item.updatedAt, item.updatedBy]),
            configs: (userConfigsS.val || []).map(item => [item.id, item.name, item.group, item.value, item.updatedAt, item.updatedBy]),
        });
        if (signature === streamSignature) return;
        streamSignature = signature;
        syncRowsFromUniverse();
    });

    const addRow = (type) => { setRows([...(rows.val || []), type === "secret" ? makeSecretRow(null) : makeConfigRow(null)]); };
    const removeRow = (row) => { setRows((rows.val || []).filter(r => r !== row)); };
    const sortValue = (row, key) => {
        if (key === "type") return row.type;
        if (key === "value") return row.type === "config" ? rawStateValue(row.value) : "";
        return rawStateValue(row.name);
    };

    const sortRows = (items) => {
        const {key, dir} = sort.val;
        const direction = dir === "desc" ? -1 : 1;
        return [...items].sort((a, b) => {
            const av = sortValue(a, key).toLowerCase();
            const bv = sortValue(b, key).toLowerCase();
            const cmp = av.localeCompare(bv) || rawStateValue(a.name).localeCompare(rawStateValue(b.name)) || a.type.localeCompare(b.type);
            return cmp * direction;
        });
    };

    const setSort = (key) => {
        const current = sort.val;
        sort.val = current.key === key
            ? {key, dir: current.dir === "asc" ? "desc" : "asc"}
            : {key, dir: "asc"};
    };

    const filteredRows = () => {
        if (!rows.val) return rows.val;
        const query = search.val.trim().toLowerCase();
        const filtered = query ? rows.val.filter(row =>
            row.type.includes(query) ||
            rawStateValue(row.name).toLowerCase().includes(query) ||
            (row.type === "config" && rawStateValue(row.value).toLowerCase().includes(query))) : rows.val;
        return sortRows(filtered);
    };

    const toggleReveal = async (row) => {
        if (row.revealed.val) {
            row.revealed.val = false;
            if (!row.isNew && !row.valueDirty.val) {
                row.value.val = "";
                row.orig.value = "";
                row.loaded.val = false;
            }
            return;
        }
        if (!row.loaded.val && !row.isNew) {
            try {
                error.val = null;
                const res = await capi.postV1SecretsReveal({name: row.orig.name});
                row.value.val = new TextDecoder().decode(res.value);
                row.orig.value = row.value.val;
                row.loaded.val = true;
            } catch (e) { error.val = e.message; return; }
        }
        row.revealed.val = true;
    };

    const saveConfigRow = async (row, name) => {
        await capi.postV1UserConfigsSet({
            name,
            group: "default",
            value: row.value.val,
        });
        if (!row.isNew && row.orig.name !== name) {
            await capi.postV1UserConfigsDelete({name: row.orig.name});
        }
    };

    const saveSecretRow = async (row, name) => {
        let value;
        if (row.loaded.val || row.valueDirty.val) {
            value = row.value.val;
        } else {
            const res = await capi.postV1SecretsReveal({name: row.orig.name});
            value = new TextDecoder().decode(res.value);
        }
        await capi.postV1SecretsSet({
            name,
            group: "default",
            value: new TextEncoder().encode(value),
        });
        if (!row.isNew && row.orig.name !== name) {
            await capi.postV1SecretsDelete({name: row.orig.name});
        }
    };

    const saveRow = async (row) => {
        const name = row.name.val.trim();
        if (!name) { error.val = `${row.type === "secret" ? "Secret" : "Config"} name is required`; return; }
        try {
            error.val = null;
            if (row.type === "secret") await saveSecretRow(row, name);
            else await saveConfigRow(row, name);
            row.isNew = false;
            row._saved = true;
            row.orig.name = name;
            if (row.type === "config") {
                row.orig.value = row.value.val;
            } else {
                row.valueDirty.val = false;
            }
            syncRowsFromUniverse();
        } catch (e) {
            error.val = e.message;
        }
    };

    const discardRow = (row) => {
        if (row.isNew) { removeRow(row); return; }
        row.name.val = row.orig.name;
        if (row.type === "config") {
            row.value.val = row.orig.value;
            return;
        }
        row.value.val = row.loaded.val ? row.orig.value : "";
        row.valueDirty.val = false;
        row.revealed.val = false;
    };

    const deleteRow = async (row) => {
        try {
            error.val = null;
            if (row.type === "secret") await capi.postV1SecretsDelete({name: row.orig.name});
            else await capi.postV1UserConfigsDelete({name: row.orig.name});
        } catch (e) {
            error.val = e.message;
        }
    };

    const unlockCode = van.state("");
    const unlock = async () => {
        try {
            error.val = null;
            await capi.postV1SecretsUnlock({code: unlockCode.val});
            unlockCode.val = "";
        } catch (e) {
            error.val = e.message;
        }
    };

    const lockedSection = () => secretsStatusS.val && !secretsStatusS.val.unlocked ? div(
        {class: "rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 flex flex-col gap-3 max-w-2xl"},
        h2({class: "text-sm font-semibold text-amber-300"}, "Secrets store is locked"),
        p({class: "text-sm text-gray-400"},
            "Configs remain available. Enter the recovery code to unlock secret listing, editing, and reveal."),
        div({class: "flex flex-col sm:flex-row gap-2"},
            input({
                class: "text-input font-mono flex-1",
                type: "text",
                placeholder: "recovery code",
                value: unlockCode,
                oninput: (e) => unlockCode.val = e.target.value,
            }),
            spinnerButton("Unlock", unlock, "btn-primary", "button",
                () => !unlockCode.val.trim())),
    ) : "";

    const cellInput = (state, placeholder, mono, extra = {}) => input({
        class: `w-full bg-transparent px-2 py-1 rounded border border-transparent ` +
            `hover:border-gray-700 focus:border-brand focus:outline-none ${mono ? "font-mono" : ""}`,
        placeholder,
        value: state,
        oninput: (e) => state.val = e.target.value,
        ...extra,
    });

    const typeBadge = (type) => span({
        class: `inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${type === "secret"
            ? "bg-purple-500/15 text-purple-300"
            : "bg-blue-500/15 text-blue-300"}`,
    }, type === "secret" ? "Secret" : "Config");

    const configValueInput = (row) => cellInput(row.value, "value", true);

    const secretValueInput = (row) => div({class: "flex items-center gap-1"},
        input({
            class: "flex-1 min-w-0 bg-transparent px-2 py-1 rounded border border-transparent " +
                "hover:border-gray-700 focus:border-brand focus:outline-none font-mono",
            type: "text",
            autocomplete: "off",
            readonly: () => !(row.isNew || row.revealed.val),
            style: () => row.revealed.val ? "" : "-webkit-text-security: disc;",
            placeholder: row.isNew ? "value" : DEFAULT_SECRET_MASK,
            value: () => row.isNew || row.revealed.val ? row.value.val : "",
            oninput: (e) => {
                if (!row.isNew && !row.revealed.val) return;
                row.value.val = e.target.value;
                row.valueDirty.val = true;
            },
        }),
        iconButton(() => row.revealed.val ? eyeOffIcon() : eyeOpenIcon(),
            () => toggleReveal(row)),
    );

    const rowEl = (row) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-1 pr-3 w-px whitespace-nowrap"}, typeBadge(row.type)),
        td({class: "py-1 pr-3 w-1/3"}, cellInput(row.name, "name", true)),
        td({class: "py-1 pr-3"}, row.type === "secret" ? secretValueInput(row) : configValueInput(row)),
        td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
            () => isDirty(row)
                ? div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Save", () => saveRow(row), "bg-brand text-white hover:bg-blue-600",
                        () => !row.name.val.trim()),
                    smallBtn("Discard", () => discardRow(row), "bg-gray-700 text-gray-200 hover:bg-gray-600"))
                : iconButton(trashIcon(), () => deleteRow(row), "hover:text-red-400")),
    );

    const sortableHeader = (key, label, cls = "") => th({class: `pb-2 pr-3 font-medium ${cls}`},
        button({
            type: "button",
            class: "inline-flex items-center gap-1 text-gray-400 hover:text-gray-100 cursor-pointer",
            onclick: () => setSort(key),
        }, label, () => sort.val.key === key ? (sort.val.dir === "asc" ? " ^" : " v") : ""));

    const contentTable = () => div(
        {class: "card h-full min-h-0 flex flex-col gap-3"},
        errorBanner,
        lockedSection,
        div({class: "flex flex-wrap items-center justify-between gap-3"},
            input({
                class: "text-input search-input",
                type: "search",
                placeholder: "Search secrets / configs",
                value: search,
                oninput: (e) => search.val = e.target.value,
            }),
            div({class: "flex flex-wrap items-center gap-2"},
                actionButton("Add secret", () => addRow("secret"), "bg-gray-700 text-gray-200 hover:bg-gray-600",
                    () => !secretsStatusS.val || !secretsStatusS.val.unlocked),
                actionButton("Add config", () => addRow("config")))),
        div({class: "flex-1 min-h-0 overflow-auto"}, () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            if (rows.val.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No secrets or configs yet.");
            }
            const visibleRows = filteredRows();
            if (visibleRows.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No secrets or configs match your search.");
            }
            return table(
                {class: "w-full text-sm"},
                thead(
                    tr({class: "text-left text-gray-400 border-b border-gray-700"},
                        sortableHeader("type", "Type", "w-px"),
                        sortableHeader("name", "Name"),
                        sortableHeader("value", "Value"),
                        th({class: "pb-2 w-px"}, ""),
                    )),
                tbody(...visibleRows.map(rowEl)),
            );
        }),
    );

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3"},
        contentTable,
    );
}
