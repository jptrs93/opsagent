import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { div, p, input, button, table, thead, tbody, tr, th, td, code } = van.tags;
const { svg, path, line } = van.tags("http://www.w3.org/2000/svg");

const svgBase = {
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round",
    class: "w-4 h-4",
};

const trashIcon = () => svg(svgBase,
    path({d: "M3 6h18"}),
    path({d: "M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"}),
    path({d: "M10 11v6M14 11v6"}),
    path({d: "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}));
const plusIcon = () => svg(svgBase, line({x1: "12", y1: "5", x2: "12", y2: "19"}), line({x1: "5", y1: "12", x2: "19", y2: "12"}));

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

export function configsPage() {
    const rows = van.state(null);
    const error = van.state(null);
    const search = van.state("");

    const errorBanner = () => error.val ? p({class: "text-red-400 text-sm"}, `Error: ${error.val}`) : "";

    const makeRow = (config) => {
        const isNew = !config;
        return {
            config, isNew, _saved: false,
            name: van.state(config ? config.name : ""),
            group: van.state(config ? config.group : ""),
            value: van.state(config ? config.value : ""),
            orig: {
                name: config ? config.name : "",
                group: config ? config.group : "",
                value: config ? config.value : "",
            },
        };
    };

    const isDirty = (row) => row.isNew
        || row.name.val !== row.orig.name
        || row.group.val !== row.orig.group
        || row.value.val !== row.orig.value;

    const reloadRows = async () => {
        const res = await capi.postV1UserConfigsList({});
        const pending = (rows.val || []).filter(r => r.isNew && !r._saved);
        rows.val = [...(res.items || []).map(makeRow), ...pending];
    };

    const reload = async () => {
        try {
            error.val = null;
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    reload();

    const addRow = () => { rows.val = [...(rows.val || []), makeRow(null)]; };
    const removeRow = (row) => { rows.val = rows.val.filter(r => r !== row); };
    const filteredRows = () => {
        if (!rows.val) return rows.val;
        const query = search.val.trim().toLowerCase();
        if (!query) return rows.val;
        return rows.val.filter(row =>
            row.name.val.toLowerCase().includes(query) ||
            row.group.val.toLowerCase().includes(query) ||
            row.value.val.toLowerCase().includes(query));
    };

    const saveRow = async (row) => {
        const name = row.name.val.trim();
        if (!name) { error.val = "Config name is required"; return; }
        try {
            error.val = null;
            await capi.postV1UserConfigsSet({
                name,
                group: row.group.val.trim(),
                value: row.value.val,
            });
            if (!row.isNew && row.orig.name !== name) {
                await capi.postV1UserConfigsDelete({name: row.orig.name});
            }
            row._saved = true;
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    const discardRow = (row) => {
        if (row.isNew) { removeRow(row); return; }
        row.name.val = row.orig.name;
        row.group.val = row.orig.group;
        row.value.val = row.orig.value;
    };

    const deleteRow = async (row) => {
        try {
            error.val = null;
            await capi.postV1UserConfigsDelete({name: row.orig.name});
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    const cellInput = (state, placeholder, mono) => input({
        class: `w-full bg-transparent px-2 py-1 rounded border border-transparent ` +
            `hover:border-gray-700 focus:border-brand focus:outline-none ${mono ? "font-mono" : ""}`,
        placeholder,
        value: state,
        oninput: (e) => state.val = e.target.value,
    });

    const rowEl = (row) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-1 pr-3 w-1/4"}, cellInput(row.name, "name", true)),
        td({class: "py-1 pr-3 w-1/6"}, cellInput(row.group, "group", false)),
        td({class: "py-1 pr-3"}, cellInput(row.value, "value", true)),
        td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
            () => isDirty(row)
                ? div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Save", () => saveRow(row), "bg-brand text-white hover:bg-blue-600",
                        () => !row.name.val.trim()),
                    smallBtn("Discard", () => discardRow(row), "bg-gray-700 text-gray-200 hover:bg-gray-600"))
                : iconButton(trashIcon(), () => deleteRow(row), "hover:text-red-400")),
    );

    const configsTable = () => div(
        {class: "card h-full min-h-0 flex flex-col gap-3"},
        errorBanner,
        div({class: "flex flex-wrap items-center justify-between gap-3"},
            p({class: "text-xs text-gray-400"},
                "Reference a config from a deployment's env value as ",
                code({class: "font-mono text-gray-300"}, "${c:name}"), "."),
            div({class: "flex items-center gap-2"},
                input({
                    class: "text-input w-64",
                    type: "search",
                    placeholder: "Search configs",
                    value: search,
                    oninput: (e) => search.val = e.target.value,
                }),
                button({
                    type: "button",
                    class: "flex items-center gap-1 whitespace-nowrap text-sm px-3 py-1.5 rounded-lg bg-gray-700 " +
                        "text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
                    onclick: addRow,
                }, plusIcon(), "Add config"))),
        div({class: "flex-1 min-h-0 overflow-auto"}, () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            if (rows.val.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No configs yet. Click “Add config”.");
            }
            const visibleRows = filteredRows();
            if (visibleRows.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No configs match your search.");
            }
            return table(
                {class: "w-full text-sm"},
                thead(
                    tr({class: "text-left text-gray-400 border-b border-gray-700"},
                        th({class: "pb-2 pr-3 font-medium"}, "Name"),
                        th({class: "pb-2 pr-3 font-medium"}, "Group"),
                        th({class: "pb-2 pr-3 font-medium"}, "Value"),
                        th({class: "pb-2 w-px"}, ""),
                    )),
                tbody(...visibleRows.map(rowEl)),
            );
        }),
    );

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3"},
        configsTable,
    );
}
