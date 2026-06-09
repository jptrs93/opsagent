import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";

const { div, h1, h2, p, span, input, button, table, thead, tbody, tr, th, td, code } = van.tags;
const { svg, path, circle, line } = van.tags("http://www.w3.org/2000/svg");

// --- icons ---

const svgBase = {
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round",
    class: "w-4 h-4",
};
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

export function secretsPage() {
    const status = van.state(null);   // {unlocked, recoveryConfigured} | null
    const rows = van.state(null);     // [rowModel] | null
    const error = van.state(null);

    // A rowModel mirrors one secret as an editable row. `orig` holds the
    // last-saved values for dirty detection; `orig.value` is meaningful only
    // once `loaded` is true (we fetch the plaintext lazily, on reveal/save).
    const makeRow = (meta) => {
        const isNew = !meta;
        return {
            meta, isNew, _saved: false,
            name: van.state(meta ? meta.name : ""),
            group: van.state(meta ? meta.group : ""),
            value: van.state(""),
            revealed: van.state(false),  // show plaintext vs masked
            loaded: van.state(isNew),    // `value` holds the real current plaintext
            valueDirty: van.state(false),
            orig: {name: meta ? meta.name : "", group: meta ? meta.group : "", value: ""},
        };
    };

    const isDirty = (row) => row.isNew
        || row.name.val !== row.orig.name
        || row.group.val !== row.orig.group
        || row.valueDirty.val;

    const loadStatus = async () => { status.val = await capi.postV1SecretsStatus({}); };

    const reloadRows = async () => {
        if (!status.val || !status.val.unlocked) { rows.val = []; return; }
        const res = await capi.postV1SecretsList({});
        const pending = (rows.val || []).filter(r => r.isNew && !r._saved);
        rows.val = [...(res.items || []).map(makeRow), ...pending];
    };

    const reload = async () => {
        try {
            error.val = null;
            await loadStatus();
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    reload();

    const addRow = () => { rows.val = [...(rows.val || []), makeRow(null)]; };
    const removeRow = (row) => { rows.val = rows.val.filter(r => r !== row); };

    const toggleReveal = async (row) => {
        if (row.revealed.val) { row.revealed.val = false; return; }
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

    const saveRow = async (row) => {
        const name = row.name.val.trim();
        if (!name) { error.val = "Secret name is required"; return; }
        try {
            error.val = null;
            // Determine the value to persist. If the operator never touched or
            // loaded the value of an existing secret, fetch it so a name/group
            // edit doesn't clobber it.
            let value;
            if (row.loaded.val || row.valueDirty.val) {
                value = row.value.val;
            } else {
                const res = await capi.postV1SecretsReveal({name: row.orig.name});
                value = new TextDecoder().decode(res.value);
            }
            await capi.postV1SecretsSet({
                name,
                group: row.group.val.trim(),
                value: new TextEncoder().encode(value),
            });
            // A rename of an existing secret is set-new + delete-old.
            if (!row.isNew && row.orig.name !== name) {
                await capi.postV1SecretsDelete({name: row.orig.name});
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
        row.value.val = row.loaded.val ? row.orig.value : "";
        row.valueDirty.val = false;
        row.revealed.val = false;
    };

    const deleteRow = async (row) => {
        try {
            error.val = null;
            await capi.postV1SecretsDelete({name: row.orig.name});
            await reloadRows();
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
            await reload();
        } catch (e) {
            error.val = e.message;
        }
    };

    // --- sections ---

    const lockedSection = () => div(
        {class: "card flex flex-col gap-3 max-w-xl"},
        h2({class: "text-lg font-semibold text-amber-400"}, "Secrets store is locked"),
        p({class: "text-sm text-gray-400"},
            "This machine has no local key to decrypt the secrets store (e.g. after restoring " +
            "a backup onto a fresh machine). Enter the recovery code to unlock it and " +
            "re-establish the local key."),
        input({
            class: "text-input font-mono",
            type: "text",
            placeholder: "recovery code",
            value: unlockCode,
            oninput: (e) => unlockCode.val = e.target.value,
        }),
        div({class: "flex gap-2"},
            spinnerButton("Unlock", unlock, "btn-primary", "button",
                () => !unlockCode.val.trim())),
    );

    const cellInput = (state, placeholder, mono, extra = {}) => input({
        class: `w-full bg-transparent px-2 py-1 rounded border border-transparent ` +
            `hover:border-gray-700 focus:border-brand focus:outline-none ${mono ? "font-mono" : ""}`,
        placeholder,
        value: state,
        oninput: (e) => state.val = e.target.value,
        ...extra,
    });

    const rowEl = (row) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-1 pr-3 w-1/4"}, cellInput(row.name, "name", true)),
        td({class: "py-1 pr-3 w-1/6"}, cellInput(row.group, "group", false)),
        td({class: "py-1 pr-3"},
            div({class: "flex items-center gap-1"},
                input({
                    class: "flex-1 min-w-0 bg-transparent px-2 py-1 rounded border border-transparent " +
                        "hover:border-gray-700 focus:border-brand focus:outline-none font-mono",
                    type: () => row.revealed.val ? "text" : "password",
                    placeholder: row.isNew ? "value" : "••••••••",
                    value: row.value,
                    oninput: (e) => { row.value.val = e.target.value; row.valueDirty.val = true; },
                }),
                iconButton(() => row.revealed.val ? eyeOffIcon() : eyeOpenIcon(),
                    () => toggleReveal(row)),
            )),
        td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
            () => isDirty(row)
                ? div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Save", () => saveRow(row), "bg-brand text-white hover:bg-blue-600",
                        () => !row.name.val.trim()),
                    smallBtn("Discard", () => discardRow(row), "bg-gray-700 text-gray-200 hover:bg-gray-600"))
                : iconButton(trashIcon(), () => deleteRow(row), "hover:text-red-400")),
    );

    const secretsTable = () => div(
        {class: "card flex flex-col gap-3"},
        div({class: "flex items-center justify-between"},
            h2({class: "text-base font-semibold"}, "Secrets"),
            button({
                type: "button",
                class: "flex items-center gap-1 text-sm px-3 py-1.5 rounded-lg bg-gray-700 " +
                    "text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
                onclick: addRow,
            }, plusIcon(), "Add secret")),
        p({class: "text-xs text-gray-400"},
            "Reference a secret from a deployment's env value as ",
            code({class: "font-mono text-gray-300"}, "${name}"), "."),
        () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            if (rows.val.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No secrets yet. Click “Add secret”.");
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
                tbody(...rows.val.map(rowEl)),
            );
        },
    );

    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Secrets"),
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : div(),
        () => {
            if (status.val === null) return p({class: "text-gray-400"}, "Loading...");
            if (!status.val.unlocked) return lockedSection();
            return div(
                {class: "flex flex-col gap-6"},
                secretsTable(),
            );
        },
    );
}
