import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {trashIcon} from "../lib/icons.js";
import {deploymentDeleted} from "../lib/deployment.js";
import {
    assetMetasS,
    deploymentsS,
    secretMetasS,
    spacesS,
    userConfigsS,
} from "../state/deployments.js";
import {domainPoliciesPage} from "./domainPolicies.js";
import {tabButton, tabStrip} from "../components/tabs.js";

const { div, h2, p, span, input, button, table, thead, tbody, tr, th, td, colgroup, col } = van.tags;

// Spaces 0 and 1 are created by the installer: 0 holds OpenDeploy's own
// internal rows and 1 is where everything lands by default. Neither can be
// renamed or removed, so their rows render read-only rather than erroring on
// submit.
const DEFAULT_SPACE_IDS = new Set([0, 1]);
const isDefaultSpace = (space) => DEFAULT_SPACE_IDS.has(Number(space?.id ?? -1));

const spaceIDOf = (item) => Number(item?.spaceId || 0);

// Secrets, configs, and assets are all immutable-version rows, so a name or key
// appears once per version. Counting distinct names is what the operator means
// by "how many secrets are in this space".
const countDistinct = (items, spaceID, keyOf) => {
    const names = new Set();
    for (const item of items || []) {
        if (!item || item.deleted) continue;
        if (spaceIDOf(item) !== spaceID) continue;
        const key = keyOf(item);
        if (key) names.add(key);
    }
    return names.size;
};

const countDeployments = (deployments, spaceID) => (deployments || []).filter((deployment) => {
    const config = deployment?.config;
    return config && !deploymentDeleted(config) && Number(config.deployment?.spaceId || 0) === spaceID;
}).length;

const smallBtn = (text, onclick, cls, disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `text-xs px-3 py-1 rounded-md font-medium transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: async (e) => { if (disabledWhen && disabledWhen()) return; await onclick(e); },
}, text);

function spacesTab() {
    const error = van.state(null);
    const search = van.state("");
    const mutatingID = van.state(0);
    const addingSpace = van.state(false);
    const newSpaceName = van.state("");
    const creating = van.state(false);
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);
    // Keyed by space id rather than name, because a rename changes the name
    // while the row stays the same row.
    const nameDrafts = new Map();

    const sortedSpaces = () => [...(spacesS.val || [])]
        .filter((space) => space && !space.deleted)
        .sort((a, b) => Number(a.id || 0) - Number(b.id || 0));

    const filteredSpaces = () => {
        const query = search.val.trim().toLowerCase();
        const spaces = sortedSpaces();
        if (!query) return spaces;
        return spaces.filter((space) => (space.name || "").toLowerCase().includes(query));
    };

    const renameSpace = async (space, name, draft) => {
        if (mutatingID.val) return;
        mutatingID.val = Number(space.id);
        try {
            error.val = null;
            const updated = await capi.postV1SpacesUpdate({id: space.id, name});
            // The state stream carries the change too, but adopting the
            // response immediately keeps the input from flicking back to the
            // old name before it arrives.
            draft.originalName.val = updated?.name || name;
            draft.name.val = updated?.name || name;
        } catch (e) {
            error.val = e.message;
            draft.name.val = draft.originalName.val;
        } finally {
            mutatingID.val = 0;
        }
    };

    const createSpace = async () => {
        const name = newSpaceName.val.trim();
        if (!name || creating.val) return;
        try {
            error.val = null;
            creating.val = true;
            await capi.postV1SpacesCreate({name});
            addingSpace.val = false;
            newSpaceName.val = "";
        } catch (e) {
            error.val = e.message;
        } finally {
            creating.val = false;
        }
    };

    const cancelCreate = () => {
        if (creating.val) return;
        addingSpace.val = false;
        newSpaceName.val = "";
    };

    const requestDelete = (space) => {
        if (mutatingID.val || isDefaultSpace(space)) return;
        deleteTarget.val = space;
    };

    const cancelDelete = () => {
        if (deleteSaving.val) return;
        deleteTarget.val = null;
    };

    const confirmDelete = async () => {
        const target = deleteTarget.val;
        if (!target || deleteSaving.val) return;
        try {
            deleteSaving.val = true;
            error.val = null;
            await capi.postV1SpacesDelete({id: target.id});
            nameDrafts.delete(Number(target.id));
            deleteTarget.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            deleteSaving.val = false;
        }
    };

    const spaceNameEditor = (space) => {
        const id = Number(space.id);
        if (isDefaultSpace(space)) {
            return div({class: "flex items-center gap-2 px-2 py-1 min-w-0"},
                span({class: "truncate text-gray-200"}, space.name || `space ${id}`),
                span({class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                    "built in"),
            );
        }
        let draft = nameDrafts.get(id);
        if (!draft) {
            draft = {originalName: van.state(space.name || ""), name: van.state(space.name || "")};
            nameDrafts.set(id, draft);
        } else if (draft.originalName.val !== (space.name || "") && draft.name.val === draft.originalName.val) {
            // Renamed somewhere else — adopt it, but never over an edit the
            // operator has in progress.
            draft.originalName.val = space.name || "";
            draft.name.val = space.name || "";
        }
        const {originalName, name} = draft;
        return inlineEditableInput({
            value: name,
            dirty: () => name.val !== originalName.val,
            valid: () => Boolean(name.val.trim()),
            disabled: () => Boolean(mutatingID.val),
            oninput: (event) => { name.val = event.target.value; },
            onSave: async () => {
                const next = name.val.trim();
                if (next === originalName.val) {
                    name.val = originalName.val;
                    return;
                }
                await renameSpace(space, next, draft);
            },
            onDiscard: () => { name.val = originalName.val; },
            inputClass: "w-full bg-transparent px-2 py-1 rounded border border-transparent hover:border-gray-700 focus:border-brand focus:outline-none text-gray-200",
            placeholder: "space name",
            ariaLabel: `Space name ${space.name || id}`,
            saveAriaLabel: `Save space name ${space.name || id}`,
            discardAriaLabel: `Discard space name change for ${space.name || id}`,
        });
    };

    const countCell = (value) => td(
        {class: "py-1 pr-3 text-gray-400 whitespace-nowrap tabular-nums"},
        String(value),
    );

    const spaceRow = (space) => {
        const id = Number(space.id);
        const deletable = !isDefaultSpace(space);
        return tr(
            {class: "data-row align-middle classic:border-gray-800 classic:last:border-0 classic:hover:bg-transparent", "data-testid": `space-row-${id}`},
            td({class: "py-1 pr-3 min-w-0"}, spaceNameEditor(space)),
            countCell(countDeployments(deploymentsS.val, id)),
            countCell(countDistinct(secretMetasS.val, id, (item) => item.key)),
            countCell(countDistinct(userConfigsS.val, id, (item) => item.key)),
            countCell(countDistinct(assetMetasS.val, id, (item) => item.key)),
            td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
                div({class: "flex items-center justify-end gap-1"},
                    button({
                        type: "button",
                        title: deletable ? `Delete space ${space.name || id}` : "Built-in spaces cannot be deleted",
                        "aria-label": deletable ? `Delete space ${space.name || id}` : "Built-in spaces cannot be deleted",
                        disabled: () => !deletable || Boolean(mutatingID.val),
                        class: () => `inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 ` +
                            `hover:bg-surface hover:text-red-400 slate:hover:bg-white/5 graphite:hover:bg-white/5 transition-colors ${!deletable || mutatingID.val
                                ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                        onclick: () => requestDelete(space),
                    }, trashIcon()),
                )),
        );
    };

    const addSpaceRow = () => addingSpace.val ? div(
        {class: "flex flex-none flex-col gap-2 border-b border-gray-700 px-3 py-2 sm:flex-row sm:items-center slate:border-line graphite:border-line"},
        input({
            class: "text-input cluster-add-input max-w-md",
            placeholder: "New space name",
            disabled: creating,
            value: newSpaceName,
            oninput: (e) => { newSpaceName.val = e.target.value; },
            onkeydown: (e) => {
                if (e.key === "Enter") void createSpace();
                if (e.key === "Escape") cancelCreate();
            },
        }),
        div({class: "flex items-center gap-2"},
            spinnerButton("Save", createSpace,
                "text-xs px-3 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600 whitespace-nowrap",
                "button", () => creating.val || !newSpaceName.val.trim()),
            smallBtn("Discard", cancelCreate, "bg-gray-700 text-gray-200 hover:bg-gray-600 slate:bg-white/[0.06] slate:hover:bg-white/10 graphite:bg-transparent graphite:text-gray-300 graphite:ring-1 graphite:ring-inset graphite:ring-line graphite:hover:bg-white/[0.04]", () => creating.val),
        ),
    ) : "";

    const headerCell = (text, cls = "pr-3") => th(
        {class: `col-head py-1.5 ${cls} classic:text-[10px] slate:h-7 slate:py-0 graphite:h-6 graphite:py-0`}, text);

    const toolbar = () => div(
        {class: "page-toolbar cluster-toolbar gap-2 classic:py-2"},
        input({
            class: "text-input search-input toolbar-input",
            type: "search",
            placeholder: "Search spaces",
            "aria-label": "Search spaces",
            value: search,
            oninput: (e) => search.val = e.target.value,
        }),
        div({class: "flex-1"}),
        button({
            type: "button",
            disabled: () => addingSpace.val,
            class: "toolbar-button",
            onclick: () => {
                if (addingSpace.val) return;
                newSpaceName.val = "";
                addingSpace.val = true;
            },
        }, "Add space"),
    );

    const tableArea = () => div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto"}, () => {
            const visible = filteredSpaces();
            if (!visible.length) {
                return p({class: "px-4 py-3 text-gray-400 text-sm"},
                    search.val.trim() ? "No spaces match your search." : "No spaces yet. Click Add space.");
            }
            return div({class: "pl-4 pr-2 slate:px-3 graphite:px-3"}, table(
                {class: "w-full table-fixed text-[13px]"},
                colgroup(
                    col({style: "width:40%"}),
                    col({style: "width:13%"}),
                    col({style: "width:12%"}),
                    col({style: "width:12%"}),
                    col({style: "width:12%"}),
                    col({style: "width:11%"}),
                ),
                thead(tr({class: "text-left border-b border-gray-800 slate:border-line graphite:border-0"},
                    headerCell("Name", "pr-3 slate:pl-2 graphite:pl-2"),
                    headerCell("Deployments"),
                    headerCell("Secrets"),
                    headerCell("Configs"),
                    headerCell("Assets"),
                    headerCell("", "w-px"))),
                tbody(...visible.map(spaceRow)),
            ));
        });

    const deleteOverlay = () => {
        const target = deleteTarget.val;
        if (!target) return "";
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div(
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "text-sm text-gray-300"},
                    `Are you sure you want to delete space ${target.name || target.id}?`),
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", cancelDelete, "bg-gray-700 text-gray-200 hover:bg-gray-600", () => deleteSaving.val),
                    spinnerButton("Confirm", confirmDelete,
                        "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500",
                        "button", () => deleteSaving.val),
                ),
            ),
        );
    };

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden"},
        toolbar(),
        addSpaceRow,
        () => error.val ? p(
            {class: "flex-none border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"},
            `Error: ${error.val}`) : "",
        tableArea,
        deleteOverlay,
    );
}

// The Spaces page: the spaces themselves, and the policies on which of them
// may publish which ingress hostnames.
export function spacesPage() {
    const tab = van.state("spaces");

    return div(
        {class: "page"},
        tabStrip({label: "Spaces"},
            tabButton({id: "spaces", activeTab: tab, title: "Spaces", testid: "spaces-tab-spaces"}),
            tabButton({id: "domain-policies", activeTab: tab, title: "Domain policies", testid: "spaces-tab-domain-policies"}),
        ),
        div(
            {class: "page-body flex flex-col overflow-hidden"},
            () => tab.val === "spaces" ? spacesTab() : domainPoliciesPage(),
        ),
    );
}
