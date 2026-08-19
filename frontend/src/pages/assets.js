import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {handleErr} from "../capi/err.js";
import {decodeAsset} from "../capi/model.js";
import {assetEditorOverlay, preloadAssetCodeEditor} from "../components/assetEditor.js";
import {loadAssetPreview} from "../lib/assetContent.js";
import {referenceUsageOverlay} from "../components/referenceUsageOverlay.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {formatDate, formatDateTime} from "../lib/date.js";
import {containerWorkload} from "../lib/deploymentConfig.js";
import {
    caretRightIcon, checkIcon, chevronDownIcon, closeIcon, columnsIcon, editIcon,
    fileIcon, folderIcon, plusIcon, searchIcon, sortArrowIcon, uploadIcon,
} from "../lib/icons.js";
import {selectableSpaces} from "../lib/nodeSpaces.js";
import {deploymentUsages} from "../lib/referenceUsage.js";
import {
    ASSET_COLUMNS, ASSET_DEFAULT_COLUMNS, ASSET_DEFAULT_COLUMN_WIDTHS,
    assetDirsAsNamed, fmtSize, makeAssetItems,
} from "../lib/assetExplorer.js";
import {
    buildRows, checkDrop, dirsById, dirPathSegments, dragSource, dropDestination,
    emptySpaceIds, flexColumnKey, folderOptions, itemKey, itemPathSegments, sameSet,
    spaceHue,
} from "../lib/valueExplorer.js";
import {assetDirectoriesS, assetMetasS, deploymentsS, machinesS, spacesS} from "../state/deployments.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {loginS} from "../state/login.js";

// selectEl: the pages define their own row-selection helper named `select`.
const {button, col, colgroup, dd, div, dl, dt, h2, input, option, p, select: selectEl, span, table, tbody, td, th, thead, tr} = van.tags;

const VIEW_STORAGE_KEY = "opendeployAssetsExplorerView";
const INSPECTOR_MIN = 220;
const INSPECTOR_MAX = 460;

export {preloadAssetCodeEditor};

const loadView = () => {
    try {
        return JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY)) || {};
    } catch (_) {
        return {};
    }
};

// Deployment specs pin asset *version* row ids; an asset's meta lists every
// published version id, so membership is the usage test.
const assetRefMatches = (versionIDs, ref) =>
    versionIDs.has(Number(ref?.assetVersionId || 0));

async function uploadAssetFile(file, params, token, onProgress) {
    const query = new URLSearchParams(params);
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `/v1/assets/upload?${query}`);
        xhr.withCredentials = true;
        xhr.responseType = "arraybuffer";
        xhr.setRequestHeader("Accept", "application/x-protobuf");
        xhr.setRequestHeader("Content-Type", "application/octet-stream");
        if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`);

        xhr.upload.onprogress = (event) => {
            onProgress(event.loaded, event.lengthComputable ? event.total : file.size);
        };
        xhr.onload = async () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                onProgress(file.size, file.size);
                resolve(decodeAsset(xhr.response));
                return;
            }
            try {
                await handleErr({
                    ok: false,
                    status: xhr.status,
                    arrayBuffer: async () => xhr.response || new ArrayBuffer(0),
                });
            } catch (e) {
                reject(e);
            }
        };
        xhr.onerror = () => reject(new Error("Asset upload failed"));
        xhr.onabort = () => reject(new Error("Asset upload cancelled"));
        xhr.send(file);
    });
}

export function assetsPage() {
    const saved = loadView();

    // The two halves of the space filter. hiddenSpaces is what was hidden by
    // hand and persists; shownEmptySpaces re-admits a space the empty-space
    // default hid, and is deliberately per-visit — landing on the page starts
    // from the default again.
    const hiddenSpaces = van.state(new Set(Array.isArray(saved.hiddenSpaces) ? saved.hiddenSpaces : []));
    const shownEmptySpaces = van.state(new Set());
    const shownCols = van.state(new Set(Array.isArray(saved.cols) ? saved.cols : ASSET_DEFAULT_COLUMNS));
    const colWidths = van.state({...ASSET_DEFAULT_COLUMN_WIDTHS, ...(saved.colWidths || {})});
    const sort = van.state(saved.sort?.key ? saved.sort : {key: "name", dir: "asc"});
    const expanded = van.state(new Set(Array.isArray(saved.expanded) ? saved.expanded : []));
    const inspectorWidth = van.state(Number(saved.inspectorWidth) || 280);
    const inspectorOpen = van.state(true);
    const selectedKey = van.state(null);
    const search = van.state("");
    const openMenu = van.state(null); // "spaces" | "cols" | null
    const error = van.state(null);
    // renameState marks which row is being renamed; the draft lives in its own
    // state that no binding reads, so typing never rebuilds the input.
    const renameState = van.state(null); // {key}
    const renameDraft = van.state("");
    const folderName = van.state("");

    const usageTarget = van.state(null);
    const editorTarget = van.state(null); // {mode, assetId, version, latestVersion}
    const folderDialog = van.state(null); // truthy while the new-folder dialog is open
    // Destination of whichever create dialog is open. It lives outside those
    // dialogs' own state so that changing the space re-renders only the picker:
    // rebuilding the dialog would discard a half-typed name or asset body.
    const createDest = van.state({spaceId: 0, directoryId: 0});
    const moveDialog = van.state(null);   // {label, options(), currentId(), apply, spaceId?: van.state}
    const moveError = van.state(null);    // {name, message} from a refused drop
    const deleteTarget = van.state(null); // {label, apply}
    const dialogSaving = van.state(false);

    // Upload runs through a hidden file picker; the context is captured before
    // the picker opens so the chosen file knows where it is going.
    let uploadContext = null;             // {mode: "new", spaceId, directoryId, location} | {mode: "version", assetId, key}
    const uploadTarget = van.state(null); // uploadContext + {file}
    const uploadName = van.state("");
    const uploadSaving = van.state(false);
    const uploadError = van.state("");
    const uploadLoaded = van.state(0);
    const uploadTotal = van.state(0);
    const uploadedKey = van.state("");

    let expandedTouched = Array.isArray(saved.expanded);

    const persistView = () => {
        try {
            localStorage.setItem(VIEW_STORAGE_KEY, JSON.stringify({
                hiddenSpaces: [...hiddenSpaces.val],
                cols: [...shownCols.val],
                colWidths: colWidths.val,
                sort: sort.val,
                expanded: [...expanded.val],
                inspectorWidth: inspectorWidth.val,
            }));
        } catch (_) { /* view state is a convenience, never load-bearing */ }
    };


    const currentItems = () => makeAssetItems(assetMetasS.val);
    const currentDirs = () => assetDirsAsNamed(assetDirectoriesS.val);
    // listedSpaces is the page's whole notion of "the spaces": the opendeploy
    // space is dropped once here so it stays out of the tree, the filter menu
    // and every destination picker without each of them re-testing for it.
    const listedSpaces = () => selectableSpaces(spacesS.val);
    const spaceName = (id) => (spacesS.val || []).find((s) => Number(s.id) === Number(id))?.name || `space ${id}`;

    // Derived, not latched at mount: a space that gains its first asset — or
    // whose contents only arrive later on the state stream — leaves the empty
    // set on its own, without the filter having to be touched.
    const emptySpaces = van.derive(() => emptySpaceIds(listedSpaces(), currentDirs(), currentItems()));
    // filteredSpaces is what the rest of the page filters on: hand-hidden
    // spaces, plus the empty ones not re-admitted this visit.
    const filteredSpaces = van.derive(() => new Set(listedSpaces().map((s) => Number(s.id)).filter((id) =>
        hiddenSpaces.val.has(id) || (emptySpaces.val.has(id) && !shownEmptySpaces.val.has(id)),
    )));
    const visibleSpaces = () => listedSpaces().filter((s) => !filteredSpaces.val.has(Number(s.id)));

    // Dirty against the landing default — empty spaces hidden — so the reset
    // row offers itself only for choices actually made here.
    const spacesDirty = () => !sameSet(filteredSpaces.val, emptySpaces.val);
    const colsDirty = () => !sameSet(shownCols.val, new Set(ASSET_DEFAULT_COLUMNS));

    const usageForItem = (item) => {
        const versionIDs = new Set((item.meta.contentVersions || []).map((ref) => Number(ref?.id || 0)).filter(Boolean));
        const deployments = deploymentUsages(deploymentsS.val, spacesS.val, machinesS.val, (deployment) => {
            const cfg = deployment?.config;
            if (!cfg || cfg.deleted) return false;
            const runtime = containerWorkload(cfg)?.runtime || {};
            return Object.values(runtime.envVars || {}).some((ref) => assetRefMatches(versionIDs, ref))
                || (runtime.assetMounts || []).some((ref) => assetRefMatches(versionIDs, ref));
        });
        return {deployments, settings: []};
    };

    const resolveSelection = () => {
        const key = selectedKey.val;
        if (!key) return null;
        if (key.startsWith("space:")) {
            const space = (spacesS.val || []).find((s) => `space:${s.id}` === key);
            return space ? {type: "space", space} : null;
        }
        if (key.startsWith("dir:")) {
            const dir = currentDirs().find((d) => `dir:${d.id}` === key);
            return dir ? {type: "dir", dir} : null;
        }
        const item = currentItems().find((i) => itemKey(i) === key);
        return item ? {type: "item", item} : null;
    };

    // First visit: open every visible space so the page reads as a populated
    // tree, not a wall of closed roots. Stops the moment the user touches any
    // disclosure. Also drops a selection stranded by the space filter — but
    // only when the row demonstrably exists and is hidden: a key the stream
    // has not echoed yet (a create still in flight) must survive.
    van.derive(() => {
        if (!expandedTouched) {
            const keys = new Set(visibleSpaces().map((s) => `space:${s.id}`));
            if (!sameSet(keys, expanded.val)) expanded.val = keys;
        }
        const key = selectedKey.val;
        if (!key) return;
        if (key.startsWith("space:")) {
            if (filteredSpaces.val.has(Number(key.slice(6)))) selectedKey.val = null;
            return;
        }
        if (key.startsWith("dir:")) {
            const dir = currentDirs().find((d) => `dir:${d.id}` === key);
            if (dir && filteredSpaces.val.has(Number(dir.spaceId))) selectedKey.val = null;
            return;
        }
        const item = currentItems().find((i) => itemKey(i) === key);
        if (item && filteredSpaces.val.has(item.spaceId)) selectedKey.val = null;
    });


    const select = (key) => {
        selectedKey.val = key;
        inspectorOpen.val = true;
        renameState.val = null;
        openMenu.val = null;
    };

    const toggleExpanded = (key) => {
        expandedTouched = true;
        const next = new Set(expanded.val);
        next.has(key) ? next.delete(key) : next.add(key);
        expanded.val = next;
        persistView();
    };

    const expandTo = (spaceId, directoryId) => {
        expandedTouched = true;
        const next = new Set(expanded.val);
        next.add(`space:${spaceId}`);
        const byId = dirsById(currentDirs());
        const seen = new Set();
        let current = Number(directoryId || 0);
        while (current !== 0 && !seen.has(current)) {
            seen.add(current);
            next.add(`dir:${current}`);
            current = Number(byId.get(current)?.parentId || 0);
        }
        expanded.val = next;
        persistView();
    };

    const setSort = (key) => {
        sort.val = sort.val.key === key
            ? {key, dir: sort.val.dir === "asc" ? "desc" : "asc"}
            : {key, dir: "asc"};
        persistView();
    };

    const createContext = () => {
        const sel = resolveSelection();
        if (sel?.type === "space") return {spaceId: Number(sel.space.id), directoryId: 0};
        if (sel?.type === "dir") return {spaceId: Number(sel.dir.spaceId), directoryId: Number(sel.dir.id)};
        if (sel?.type === "item") return {spaceId: sel.item.spaceId, directoryId: sel.item.directoryId};
        const first = visibleSpaces()[0] || listedSpaces()[0];
        return {spaceId: first ? Number(first.id) : 1, directoryId: 0};
    };

    const locationLabel = ({spaceId, directoryId}) => {
        const segments = dirPathSegments(dirsById(currentDirs()), directoryId);
        return `${spaceName(spaceId)}/${segments.length ? segments.join("/") + "/" : ""}`;
    };

    const openEditor = (item, version = 0) => {
        select(itemKey(item));
        editorTarget.val = {
            mode: "edit",
            assetId: item.id,
            version: version || item.version,
            latestVersion: item.version,
        };
    };

    const openCreate = () => {
        createDest.val = createContext();
        editorTarget.val = {mode: "create"};
    };

    const openNewFolder = () => {
        createDest.val = createContext();
        folderName.val = "";
        folderDialog.val = true;
    };

    const createFolder = async () => {
        const {spaceId, directoryId} = createDest.val;
        const key = folderName.val.trim();
        if (!folderDialog.val || !key || dialogSaving.val) return;
        dialogSaving.val = true;
        try {
            error.val = null;
            const dir = await capi.postV1AssetDirectoriesCreate({spaceId, parentId: directoryId, key});
            folderDialog.val = null;
            expandTo(spaceId, dir.id);
            selectedKey.val = `dir:${dir.id}`;
        } catch (e) {
            error.val = e.message;
        } finally {
            dialogSaving.val = false;
        }
    };

    const startRename = (key, current) => {
        renameDraft.val = current;
        renameState.val = {key};
    };

    const applyRename = async () => {
        const state = renameState.val;
        const sel = resolveSelection();
        if (!state || !sel || dialogSaving.val) return;
        const name = renameDraft.val.trim();
        if (!name) return;
        dialogSaving.val = true;
        try {
            error.val = null;
            if (sel.type === "dir") {
                await capi.postV1AssetDirectoriesRename({directoryId: Number(sel.dir.id), newKey: name});
            } else if (sel.type === "item") {
                await capi.postV1AssetsRename({assetId: sel.item.id, newKey: name});
            }
            renameState.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            dialogSaving.val = false;
        }
    };

    const openMoveDialog = (sel) => {
        if (sel.type === "dir") {
            // Folders never offer a space picker: a subtree move stays
            // unsupported on the server.
            const dir = sel.dir;
            moveDialog.val = {
                label: `Move ${dir.name}`,
                options: () => folderOptions(currentDirs(), dir.spaceId, Number(dir.id)),
                currentId: () => Number(dir.parentId || 0),
                apply: async (destination) => {
                    await capi.postV1AssetDirectoriesMove({directoryId: Number(dir.id), newParentId: destination});
                    expandTo(Number(dir.spaceId), destination);
                },
            };
            return;
        }
        // Assets can change space: the picker swaps the folder list to the chosen
        // space's tree, and a changed space rides the move request. The server
        // refuses if a deployment outside the destination still pins a version.
        const item = sel.item;
        const spaceId = van.state(Number(item.spaceId));
        moveDialog.val = {
            label: `Move ${item.name}`,
            spaceId,
            options: () => folderOptions(currentDirs(), spaceId.val),
            currentId: () => (spaceId.val === Number(item.spaceId) ? item.directoryId : null),
            apply: async (destination) => {
                const request = {assetId: item.id, assetDirectoryId: destination};
                if (spaceId.val !== Number(item.spaceId)) request.spaceId = spaceId.val;
                await capi.postV1AssetsMove(request);
                expandTo(spaceId.val, destination);
            },
        };
    };

    const applyMove = async (destination) => {
        const dialog = moveDialog.val;
        if (!dialog || dialogSaving.val) return;
        dialogSaving.val = true;
        try {
            error.val = null;
            await dialog.apply(destination);
            moveDialog.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            dialogSaving.val = false;
        }
    };

    const openDelete = (sel) => {
        if (sel.type === "dir") {
            deleteTarget.val = {
                label: `folder ${sel.dir.name}`,
                apply: async () => {
                    await capi.postV1AssetDirectoriesDelete({directoryId: Number(sel.dir.id)});
                    selectedKey.val = null;
                },
            };
            return;
        }
        const item = sel.item;
        deleteTarget.val = {
            label: `asset ${item.name}`,
            apply: async () => {
                await capi.postV1AssetsDelete({assetId: item.id});
                selectedKey.val = null;
            },
        };
    };

    const confirmDelete = async () => {
        const target = deleteTarget.val;
        if (!target || dialogSaving.val) return;
        dialogSaving.val = true;
        try {
            error.val = null;
            await target.apply();
            deleteTarget.val = null;
        } catch (e) {
            error.val = e.message;
            deleteTarget.val = null;
        } finally {
            dialogSaving.val = false;
        }
    };


    const uploadPicker = input({
        class: "hidden",
        type: "file",
        onchange: (e) => {
            const file = e.target.files?.[0] || null;
            e.target.value = "";
            const context = uploadContext;
            uploadContext = null;
            if (!file || !context) return;
            uploadName.val = context.mode === "version" ? context.key : file.name;
            uploadError.val = "";
            uploadSaving.val = false;
            uploadLoaded.val = 0;
            uploadTotal.val = file.size;
            uploadedKey.val = "";
            uploadTarget.val = {...context, file};
        },
    });

    const pickUploadNew = () => {
        const context = createContext();
        uploadContext = {mode: "new", ...context, location: locationLabel(context)};
        uploadPicker.click();
    };

    const pickUploadVersion = (item) => {
        select(itemKey(item));
        uploadContext = {mode: "version", assetId: item.id, key: item.name, spaceId: item.spaceId, directoryId: item.directoryId};
        uploadPicker.click();
    };

    const closeUpload = () => {
        if (uploadSaving.val) return;
        uploadTarget.val = null;
        uploadError.val = "";
    };

    const runUpload = async () => {
        const target = uploadTarget.val;
        if (!target || uploadSaving.val) return;
        const name = uploadName.val.trim();
        if (target.mode === "new" && !name) {
            uploadError.val = "Asset name is required";
            return;
        }
        try {
            error.val = null;
            uploadError.val = "";
            uploadSaving.val = true;
            uploadLoaded.val = 0;
            uploadTotal.val = target.file.size;
            uploadedKey.val = "";
            const params = target.mode === "version"
                ? {asset_id: String(target.assetId)}
                : {name, space_id: String(target.spaceId), directory_id: String(target.directoryId)};
            const version = await uploadAssetFile(target.file, params, loginS.val?.token, (loaded, total) => {
                uploadLoaded.val = loaded;
                uploadTotal.val = total || target.file.size;
            });
            uploadedKey.val = version.fs?.key || "";
            uploadName.val = version.fs?.key || "";
            expandTo(target.spaceId, target.directoryId);
            selectedKey.val = `asset:${version.id}`;
        } catch (e) {
            uploadError.val = e.message;
        } finally {
            uploadSaving.val = false;
        }
    };


    const startColResize = (event, colKey, min) => {
        event.preventDefault();
        event.stopPropagation();
        const colEl = event.target.closest("table")?.querySelector(`col[data-col="${colKey}"]`);
        const startX = event.clientX;
        const startW = colWidths.val[colKey] ?? ASSET_DEFAULT_COLUMN_WIDTHS[colKey];
        let width = startW;
        const move = (ev) => {
            width = Math.max(min, startW + (ev.clientX - startX));
            if (colEl) colEl.style.width = `${width}px`;
        };
        const up = () => {
            document.removeEventListener("mousemove", move);
            document.removeEventListener("mouseup", up);
            document.body.classList.remove("resizing");
            colWidths.val = {...colWidths.val, [colKey]: width};
            persistView();
        };
        document.addEventListener("mousemove", move);
        document.addEventListener("mouseup", up);
        document.body.classList.add("resizing");
    };

    const nudgeColWidth = (event, colKey, min) => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        const step = event.shiftKey ? 48 : 16;
        const current = colWidths.val[colKey] ?? ASSET_DEFAULT_COLUMN_WIDTHS[colKey];
        colWidths.val = {...colWidths.val, [colKey]: Math.max(min, current + (event.key === "ArrowRight" ? step : -step))};
        persistView();
    };

    const startInspectorResize = (event) => {
        event.preventDefault();
        event.stopPropagation();
        const pane = event.target.parentElement;
        const startX = event.clientX;
        const startW = inspectorWidth.val;
        let width = startW;
        const move = (ev) => {
            width = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, startW - (ev.clientX - startX)));
            if (pane) pane.style.width = `${width}px`;
        };
        const up = () => {
            document.removeEventListener("mousemove", move);
            document.removeEventListener("mouseup", up);
            document.body.classList.remove("resizing");
            inspectorWidth.val = width;
            persistView();
        };
        document.addEventListener("mousemove", move);
        document.addEventListener("mouseup", up);
        document.body.classList.add("resizing");
    };


    const spaceDot = (spaceId) => span({
        class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
        style: `background:${spaceHue(spaceId)}`,
    });

    const assetIcon = (extra = "") => fileIcon({
        class: `w-[13px] h-[13px] flex-none text-asset ${extra}`, role: "img", "aria-label": "Asset",
    });

    const destFolderLabel = () => {
        const segments = dirPathSegments(dirsById(currentDirs()), createDest.val.directoryId);
        return `/${segments.length ? segments.join("/") + "/" : ""}`;
    };

    // Bound as a function so a space change re-renders the picker alone. Picking
    // a space drops the folder back to that space's root: directory ids belong
    // to one space, so the folder the dialog opened on cannot come along.
    const destinationPicker = () => {
        const spaces = selectEl({
            class: "input py-0.5 text-xs",
            "aria-label": "Destination space",
            onchange: (e) => { createDest.val = {spaceId: Number(e.target.value), directoryId: 0}; },
        }, ...listedSpaces().map((space) => option({value: String(space.id)}, space.name)));
        // Assigned after the options exist: a value set on an empty select is
        // discarded rather than remembered.
        spaces.value = String(createDest.val.spaceId);
        return div({class: "flex min-w-0 items-center gap-1.5"},
            spaceDot(createDest.val.spaceId),
            spaces,
            span({class: "min-w-0 truncate font-mono text-xs text-gray-500", title: destFolderLabel()}, destFolderLabel()));
    };

    const iconButton = (child, onclick, attrs = {}) => button({
        type: "button",
        ...attrs,
        class: `inline-flex h-6 w-6 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 ` +
            `hover:bg-white/10 transition-colors cursor-pointer ${attrs.class || ""}`,
        onclick: (e) => { e.stopPropagation(); onclick(e); },
    }, child);

    const actionButton = (text, onclick, cls = "bg-gray-700 text-gray-200 hover:bg-gray-600", attrs = {}) => button({
        type: "button",
        ...attrs,
        class: () => `text-xs px-2.5 py-1.5 rounded-md font-medium transition-colors cursor-pointer whitespace-nowrap ${cls} ` +
            `${typeof attrs.disabledWhen === "function" && attrs.disabledWhen() ? "opacity-50 cursor-not-allowed" : ""}`,
        onclick: async (e) => {
            if (typeof attrs.disabledWhen === "function" && attrs.disabledWhen()) return;
            await onclick(e);
        },
    }, text);


    // label is a function so the button face stays live (space dots) without
    // rebuilding the toolbar and losing search focus.
    const filterButton = ({menu, dirty, label, ariaLabel}) => button({
        type: "button",
        "aria-haspopup": "true",
        "aria-expanded": () => String(openMenu.val === menu),
        "aria-label": ariaLabel,
        class: () => `inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
            (dirty() ? "text-gray-100 border-brand" : "text-gray-400 border-gray-600 hover:bg-surface-hover hover:text-gray-100"),
        onclick: (e) => {
            e.stopPropagation();
            openMenu.val = openMenu.val === menu ? null : menu;
        },
    }, () => span({class: "inline-flex items-center gap-1.5"}, ...label()));

    const menuShell = (...children) => div({
        class: "absolute top-full left-0 z-30 mt-1.5 min-w-52 rounded-md border border-gray-600 bg-surface p-1 shadow-2xl flex flex-col",
        onclick: (e) => e.stopPropagation(),
    }, ...children);

    const menuRow = (onclick, ...children) => button({
        type: "button",
        class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
        onclick,
    }, ...children);

    const menuCheck = (on) => checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${on ? "" : "invisible"}`});
    const menuHeader = (text) => p({class: "px-2 pt-1 pb-0.5 text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, text);
    const menuTail = (text) => span({class: "ml-auto pl-3 text-[10px] text-gray-500"}, text);

    const resetRow = (onclick) => [
        div({class: "my-1 border-t border-gray-700"}),
        menuRow(onclick, closeIcon({class: "w-3.5 h-3.5 flex-none text-brand"}), "Reset to default"),
    ];

    // A row ticks and unticks against the effective filter, so an empty space
    // hidden by default takes one click to bring back — the tick just has to
    // write to whichever half of the filter is holding it out.
    const toggleSpace = (id) => {
        const hidden = new Set(hiddenSpaces.val);
        const shownEmpty = new Set(shownEmptySpaces.val);
        if (filteredSpaces.val.has(id)) {
            hidden.delete(id);
            shownEmpty.add(id);
            expandTo(id, 0);
        } else {
            hidden.add(id);
            shownEmpty.delete(id);
        }
        hiddenSpaces.val = hidden;
        shownEmptySpaces.val = shownEmpty;
        persistView();
    };

    const spacesMenu = () => menuShell(
        ...listedSpaces().map((space) => menuRow(
            () => toggleSpace(Number(space.id)),
            menuCheck(!filteredSpaces.val.has(Number(space.id))),
            spaceDot(space.id),
            span({class: "font-mono"}, space.name),
            ...(emptySpaces.val.has(Number(space.id)) ? [menuTail("empty")] : []),
        )),
        ...(spacesDirty() ? resetRow(() => {
            hiddenSpaces.val = new Set();
            shownEmptySpaces.val = new Set();
            persistView();
        }) : []),
    );

    const colsMenu = () => menuShell(
        menuHeader("Columns"),
        button({
            type: "button", disabled: true,
            class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-500",
        }, menuCheck(true), "Name", menuTail("always")),
        ...ASSET_COLUMNS.filter((c) => c.key !== "name").map((c) => menuRow(() => {
            const next = new Set(shownCols.val);
            next.has(c.key) ? next.delete(c.key) : next.add(c.key);
            shownCols.val = next;
            persistView();
        }, menuCheck(shownCols.val.has(c.key)), c.label || "Actions")),
        ...(colsDirty() ? resetRow(() => {
            shownCols.val = new Set(ASSET_DEFAULT_COLUMNS);
            persistView();
        }) : []),
    );

    const toolbar = () => div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        div({class: "relative"},
            searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
            input({
                class: "text-input search-input search-input-iconed",
                type: "search",
                placeholder: "Search assets",
                "aria-label": "Search assets",
                value: search,
                oninput: (e) => { search.val = e.target.value; },
            })),
        span({class: "relative inline-flex"},
            filterButton({
                menu: "spaces",
                dirty: spacesDirty,
                ariaLabel: "Filter spaces",
                label: () => [
                    span({class: "inline-flex items-center gap-1"}, ...visibleSpaces().map((s) => spaceDot(s.id))),
                    `${visibleSpaces().length} space${visibleSpaces().length === 1 ? "" : "s"}`,
                    chevronDownIcon({class: "w-3 h-3"}),
                ],
            }),
            () => openMenu.val === "spaces" ? spacesMenu() : ""),
        span({class: "relative inline-flex"},
            filterButton({
                menu: "cols",
                dirty: colsDirty,
                ariaLabel: "Choose columns",
                label: () => [columnsIcon({class: "w-3.5 h-3.5"}), "Columns", chevronDownIcon({class: "w-3 h-3"})],
            }),
            () => openMenu.val === "cols" ? colsMenu() : ""),
        div({class: "flex-1"}),
        button({
            type: "button",
            class: "flex items-center gap-1.5 whitespace-nowrap rounded-lg border border-gray-600 px-3 py-1.5 text-sm text-gray-300 hover:bg-surface-hover transition-colors cursor-pointer",
            onclick: openNewFolder,
        }, folderIcon({class: "w-4 h-4"}), "New folder"),
        button({
            type: "button",
            class: "flex items-center gap-1.5 whitespace-nowrap rounded-lg bg-gray-700 px-3 py-1.5 text-sm text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
            onclick: pickUploadNew,
        }, uploadIcon({class: "w-4 h-4"}), "Upload asset"),
        button({
            type: "button",
            class: "flex items-center gap-1 whitespace-nowrap rounded-lg bg-brand px-3 py-1.5 text-sm text-white hover:bg-blue-600 transition-colors cursor-pointer",
            onclick: openCreate,
        }, plusIcon(), "New asset"),
    );


    const activeColumns = () => ASSET_COLUMNS.filter((c) => shownCols.val.has(c.key));

    const headerCell = (column, flexKey, lastKey) => {
        const grip = (column.key === flexKey || column.key === lastKey) ? "" : span({
            class: "colgrip",
            tabindex: "0",
            role: "separator",
            "aria-orientation": "vertical",
            "aria-label": `Resize ${column.label || "actions"} column`,
            onclick: (e) => e.stopPropagation(),
            onmousedown: (e) => startColResize(e, column.key, column.min),
            onkeydown: (e) => nudgeColWidth(e, column.key, column.min),
        });
        const base = "sticky top-0 z-[1] bg-surface px-2 py-1.5 text-left text-[10.5px] font-semibold uppercase tracking-wider " +
            "whitespace-nowrap shadow-[inset_0_-1px_0_#374151]";
        if (column.noSort) {
            return th({class: `${base} text-right text-gray-500`}, column.label, grip);
        }
        const active = sort.val.key === column.key;
        return th({
            class: `${base} group/th cursor-pointer select-none ${active ? "text-gray-100" : "text-gray-500 hover:text-gray-300"} ${column.num ? "text-right" : ""}`,
            ...(active ? {"aria-sort": sort.val.dir === "desc" ? "descending" : "ascending"} : {}),
            onclick: (e) => {
                if (e.target.closest?.(".colgrip")) return;
                setSort(column.key);
            },
        },
        span({class: `inline-flex items-center gap-1 ${column.num ? "flex-row-reverse" : ""}`},
            column.label,
            active
                ? sortArrowIcon({class: `w-2.5 h-2.5 text-brand ${sort.val.dir === "desc" ? "rotate-180" : ""}`})
                : sortArrowIcon({class: "w-2.5 h-2.5 text-gray-600 opacity-0 group-hover/th:opacity-100 transition-opacity"})),
        grip);
    };

    // The drag bookkeeping is plain variables rather than van states on purpose.
    // The table is one derived node, so a state read here would rebuild every
    // row on each dragover — hundreds of times per drag. The hover affordance is
    // written straight onto the row element for the same reason.
    let dragging = null;   // dragSource() of the row being dragged
    let dropRow = null;    // the <tr> currently marked as the destination
    let springKey = null;  // row key the spring-load timer is counting for
    let springTimer = 0;

    const DROP_MARK = ["bg-brand/20", "outline-1", "-outline-offset-1", "outline-brand"];
    const SPRING_MS = 600;

    const clearSpring = () => {
        if (springTimer) clearTimeout(springTimer);
        springTimer = 0;
        springKey = null;
    };

    const markDropRow = (element) => {
        if (dropRow === element) return;
        if (dropRow) dropRow.classList.remove(...DROP_MARK);
        dropRow = element;
        if (dropRow) dropRow.classList.add(...DROP_MARK);
    };

    const endDrag = () => {
        dragging = null;
        markDropRow(null);
        clearSpring();
    };

    // Spring-loaded folders: hovering a closed folder or space opens it, so a
    // drag can reach anywhere in the tree. Without it only already-expanded
    // destinations are reachable, and the tree starts collapsed.
    const springLoad = (row) => {
        if (row.type === "item" || row.expanded) { clearSpring(); return; }
        if (springKey === row.key) return;
        clearSpring();
        springKey = row.key;
        springTimer = setTimeout(() => {
            springTimer = 0;
            springKey = null;
            if (dragging) toggleExpanded(row.key);
        }, SPRING_MS);
    };

    const verdictFor = (row) => checkDrop({
        dirs: currentDirs(),
        items: currentItems(),
        drag: dragging,
        destination: dropDestination(row),
    });

    const performDrop = async (drag, destination) => {
        try {
            if (drag.type === "dir") {
                await capi.postV1AssetDirectoriesMove({
                    directoryId: drag.id, newParentId: destination.directoryId, spaceId: destination.spaceId,
                });
            } else {
                await capi.postV1AssetsMove({
                    assetId: drag.id, assetDirectoryId: destination.directoryId, spaceId: destination.spaceId,
                });
            }
            expandTo(destination.spaceId, destination.directoryId);
            selectedKey.val = drag.key;
        } catch (e) {
            // Cross-space drops land here until the server supports them. The
            // reason is whatever the server said, not a guess made up front.
            moveError.val = {name: drag.name, message: e.message};
        }
    };

    const dndProps = (row) => {
        const props = {
            ondragover: (e) => {
                if (!dragging || !verdictFor(row).ok) { markDropRow(null); clearSpring(); return; }
                // preventDefault is what makes this row a drop target at all;
                // withholding it is how an invalid row refuses the drag.
                e.preventDefault();
                e.dataTransfer.dropEffect = "move";
                markDropRow(e.currentTarget);
                springLoad(row);
            },
            ondragleave: (e) => {
                // dragleave bubbles from the cells, so moving between two cells
                // of the same row would otherwise unmark and remark it.
                if (e.currentTarget.contains(e.relatedTarget)) return;
                if (dropRow === e.currentTarget) markDropRow(null);
                if (springKey === row.key) clearSpring();
            },
            ondrop: (e) => {
                e.preventDefault();
                const drag = dragging;
                const destination = dropDestination(row);
                const allowed = Boolean(drag) && verdictFor(row).ok;
                endDrag();
                if (allowed) void performDrop(drag, destination);
            },
        };
        const source = dragSource(row);
        if (!source) return props; // spaces receive drops but do not move
        props.draggable = "true";
        props.ondragstart = (e) => {
            dragging = source;
            e.dataTransfer.effectAllowed = "move";
            // Firefox refuses to start a drag with no payload.
            e.dataTransfer.setData("text/plain", source.key);
            // A full-width row ghost is unreadable; the name cell alone reads as
            // the thing being dragged.
            const cell = e.currentTarget.firstElementChild;
            if (cell) e.dataTransfer.setDragImage(cell, 12, 12);
            // On document, not the row: spring-loading rebuilds the table, so
            // the row this drag started on may be gone by the time it ends.
            document.addEventListener("dragend", endDrag, {once: true});
        };
        return props;
    };

    const namePad = (depth) => `padding-left:${0.5 + depth * 1.15}rem`;

    const disclosure = (open, key) => button({
        type: "button",
        "aria-label": open ? "Collapse" : "Expand",
        class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
        onclick: (e) => { e.stopPropagation(); toggleExpanded(key); },
    }, caretRightIcon({class: `w-[11px] h-[11px] transition-transform ${open ? "rotate-90" : ""}`}));

    const countTag = (count) => span({class: "font-mono text-[10.5px] text-gray-500 flex-none"}, String(count));
    const nameText = (text, cls = "text-gray-100") => span({class: `truncate min-w-0 ${cls}`}, text);
    const blankCells = (columns) => columns.slice(1).map(() => td({class: "border-b border-gray-800/80 px-2 py-1"}));

    // Colors match the design mock: everything sits on the card surface, rows
    // hover with the lighter surface-hover tint, and space roots are slightly
    // recessed toward the page background.
    const rowClass = (row) => {
        if (selectedKey.val === row.key) return "group cursor-default bg-brand/15";
        return `group cursor-default hover:bg-gray-700/35 ${row.type === "space" ? "bg-gray-950/30" : ""}`;
    };

    const groupRow = (row, columns, ...inner) => tr(
        {class: rowClass(row), onclick: () => select(row.key), ...dndProps(row)},
        td({class: "border-b border-gray-800/80 py-1 pr-2 font-mono text-[13px] whitespace-nowrap overflow-hidden", style: namePad(row.depth)},
            span({class: "flex items-center gap-1.5 min-w-0"}, ...inner)),
        ...blankCells(columns),
    );

    const itemActionsCell = (item) => td(
        {class: "border-b border-gray-800/80 px-1 py-0.5 text-right whitespace-nowrap"},
        span({class: () => `inline-flex justify-end gap-0.5 transition-opacity ${selectedKey.val === itemKey(item) ? "opacity-100" : "opacity-40 group-hover:opacity-100"}`},
            iconButton(editIcon({class: "w-3.5 h-3.5"}), () => openEditor(item), {
                title: `Edit asset ${item.name}`,
                "aria-label": `Edit asset ${item.name}`,
            }),
            iconButton(uploadIcon({class: "w-3.5 h-3.5"}), () => pickUploadVersion(item), {
                title: `Upload new version of ${item.name}`,
                "aria-label": `Upload new version of ${item.name}`,
            })),
    );

    const usesCell = (item, usesMap) => {
        const count = usesMap.get(itemKey(item)) || 0;
        if (!count) return "0";
        return button({
            type: "button",
            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
            "aria-label": `Show usage for asset ${item.name}`,
            onclick: (e) => {
                e.stopPropagation();
                usageTarget.val = {resourceName: item.name, ...usageForItem(item)};
            },
        }, String(count));
    };

    const itemCell = (column, item, usesMap) => {
        const base = "border-b border-gray-800/80 px-2 py-1 whitespace-nowrap overflow-hidden text-gray-400";
        if (column.key === "version") return td({class: `${base} text-right tabular-nums`}, `v${item.version}`);
        if (column.key === "created") return td({class: base, title: formatDateTime(item.createdAt, "")}, formatDate(item.createdAt, "-"));
        if (column.key === "uses") return td({class: `${base} text-right tabular-nums`}, usesCell(item, usesMap));
        if (column.key === "size") return td({class: `${base} text-right tabular-nums`}, fmtSize(item.sizeBytes));
        if (column.key === "actions") return itemActionsCell(item);
        return td({class: base});
    };

    const itemRow = (row, columns, usesMap) => tr(
        {class: rowClass(row), onclick: () => select(row.key), ...dndProps(row)},
        td({class: "border-b border-gray-800/80 py-1 pr-2 font-mono text-[13px] whitespace-nowrap overflow-hidden", style: namePad(row.depth)},
            span({class: "flex items-center gap-1.5 min-w-0"},
                span({class: "w-4 flex-none"}),
                assetIcon(),
                nameText(row.item.name))),
        ...columns.slice(1).map((column) => itemCell(column, row.item, usesMap)),
    );

    const emptyState = (text) => div({class: "flex-1 min-h-0 p-6 text-sm text-gray-500"}, text);

    const tableArea = () => {
        const spaces = visibleSpaces();
        if (!spaces.length) {
            return emptyState("No spaces shown. Add one from the Spaces filter.");
        }
        const items = currentItems();
        const usesMap = new Map(items.map((item) => [itemKey(item), usageForItem(item).deployments.length]));
        const {rows} = buildRows({
            spaces: listedSpaces(),
            dirs: currentDirs(),
            items,
            hiddenSpaceIds: filteredSpaces.val,
            query: search.val,
            expanded: expanded.val,
            sort: sort.val,
            usesByKey: usesMap,
        });
        if (search.val.trim() && rows.every((row) => row.type === "space" && row.count === 0)) {
            return emptyState("Nothing matches your search.");
        }
        const columns = activeColumns();
        const flexKey = flexColumnKey(shownCols.val, ASSET_COLUMNS);
        const lastKey = columns.length ? columns[columns.length - 1].key : null;
        return div(
            {class: "app-scroll flex-1 min-h-0 overflow-auto"},
            table(
                {class: "w-full table-fixed border-separate border-spacing-0 text-sm"},
                colgroup(...columns.map((c) => c.key === flexKey
                    ? col({"data-col": c.key})
                    : col({"data-col": c.key, style: `width:${colWidths.val[c.key] ?? ASSET_DEFAULT_COLUMN_WIDTHS[c.key]}px`}))),
                thead(tr(...columns.map((c) => headerCell(c, flexKey, lastKey)))),
                tbody(...rows.map((row) => {
                    if (row.type === "space") {
                        return groupRow(row, columns,
                            disclosure(row.expanded, row.key),
                            spaceDot(row.space.id),
                            nameText(row.space.name, "text-gray-100 font-semibold"),
                            countTag(row.count));
                    }
                    if (row.type === "dir") {
                        return groupRow(row, columns,
                            disclosure(row.expanded, row.key),
                            folderIcon({class: "w-[13px] h-[13px] flex-none text-slate-400"}),
                            nameText(row.dir.name),
                            countTag(row.count));
                    }
                    return itemRow(row, columns, usesMap);
                })),
            ),
        );
    };


    const pathbar = () => {
        const sel = resolveSelection();
        let parts = [];
        if (sel?.type === "space") parts = [{text: sel.space.name, spaceId: sel.space.id}];
        if (sel?.type === "dir") {
            parts = [{text: spaceName(sel.dir.spaceId), spaceId: sel.dir.spaceId},
                ...dirPathSegments(dirsById(currentDirs()), sel.dir.id).map((text) => ({text}))];
        }
        if (sel?.type === "item") {
            parts = [{text: spaceName(sel.item.spaceId), spaceId: sel.item.spaceId},
                ...itemPathSegments(dirsById(currentDirs()), sel.item).map((text) => ({text}))];
        }
        if (!parts.length) return "";
        return div(
            {
                class: "flex flex-none items-center gap-1.5 border-t border-gray-700 bg-gray-950/40 px-3 py-1.5 font-mono text-[11px] text-gray-500",
                "data-testid": "explorer-pathbar",
            },
            ...parts.flatMap((part, i) => [
                i === 0 ? spaceDot(part.spaceId) : span({class: "opacity-60"}, "/"),
                span({class: i === parts.length - 1 ? "text-gray-300 font-medium" : ""}, part.text),
            ]),
        );
    };


    const kvRow = (label, value) => [
        dt({class: "text-[10.5px] font-semibold uppercase tracking-wide text-gray-500"}, label),
        dd({class: "m-0 min-w-0 break-all font-mono text-xs text-gray-300"}, value),
    ];

    const inspectorTitle = (sel, currentName) => {
        const state = renameState.val;
        if (state && state.key === selectedKey.val) {
            return div({class: "flex items-center gap-1.5"},
                input({
                    // The state object, not .val: reading .val here would make
                    // the inspector rebuild (and drop focus) on every keystroke.
                    class: "input min-w-0 flex-1 py-0.5 font-mono text-xs",
                    value: renameDraft,
                    "aria-label": "New name",
                    oninput: (e) => { renameDraft.val = e.target.value; },
                    onkeydown: (e) => {
                        if (e.key === "Enter") void applyRename();
                        if (e.key === "Escape") renameState.val = null;
                    },
                }),
                iconButton(checkIcon({class: "w-3.5 h-3.5 text-green-400"}), () => { void applyRename(); }, {title: "Save name", "aria-label": "Save name"}),
                iconButton(closeIcon({class: "w-3.5 h-3.5"}), () => { renameState.val = null; }, {title: "Cancel rename", "aria-label": "Cancel rename"}));
        }
        const path = sel.type === "item"
            ? itemPathSegments(dirsById(currentDirs()), sel.item).join("/")
            : sel.type === "dir"
                ? dirPathSegments(dirsById(currentDirs()), sel.dir.id).join("/")
                : currentName;
        return p({class: "break-all font-mono text-xs text-white"}, path);
    };

    const badge = (text, cls) => span({class: `inline-flex rounded-full px-2 py-0.5 text-[10.5px] font-semibold ${cls}`}, text);

    const inspectorSpaceTag = (spaceId) => span(
        {class: "inline-flex items-center gap-1.5 font-mono text-[11px] text-gray-400"},
        spaceDot(spaceId), spaceName(spaceId));

    // "unknown" covers system-written rows and anything from before user
    // attribution existed, so the author slot never silently vanishes.
    const versionAuthor = (id) => resolveUserDisplayName(Number(id)) || "unknown";

    // table-fixed and the colgroup are what make the columns line up: under
    // automatic layout a browser sizes columns from their content and ignores
    // max-width on cells, so widths drifted with whatever name or size a row
    // happened to carry, and the author cell never truncated. Fixed layout takes
    // the widths from here instead, leaving the author column the slack.
    //
    // Four columns is what fits: the inspector is 220-460px wide, and a fifth
    // "current" column — empty on every row but the first — left the author
    // nothing. The newest version is marked by its version number instead.
    const versionsList = (item) => table({class: "w-full table-fixed font-mono text-[11px] text-gray-400"},
        // Sized to the widest real string in each column at 11px mono, plus the
        // pr-2 gutter: "v123", "Sep 30, 2026", "12.34 MB". The author takes the
        // rest — ~10 characters at the default 280px inspector, more as it widens.
        colgroup(col({style: "width:2.1rem"}), col({style: "width:5.7rem"}), col({style: "width:3.9rem"}), col()),
        tbody(...(item.meta.contentVersions || []).map((ref, i) => tr(
            {
                class: "cursor-pointer hover:bg-white/5",
                title: `Open v${ref.version}`,
                // The row replaced a <button>, so it carries the button role and
                // key handling itself to keep version history keyboard-reachable.
                role: "button",
                tabindex: "0",
                onclick: () => openEditor(item, Number(ref.version)),
                onkeydown: (e) => {
                    if (e.key !== "Enter" && e.key !== " ") return;
                    e.preventDefault();
                    openEditor(item, Number(ref.version));
                },
            },
            td({
                class: `truncate py-0.5 pr-2 font-medium ${i === 0 ? "text-green-400" : "text-gray-200"}`,
                title: i === 0 ? "Current version" : "",
            }, `v${ref.version}`),
            td({class: "truncate py-0.5 pr-2", title: formatDateTime(ref.createdAt, "")},
                formatDate(ref.createdAt, "-")),
            td({class: "truncate py-0.5 pr-2 text-right text-gray-500"}, fmtSize(ref.sizeBytes)),
            td({class: "truncate py-0.5 text-gray-500", title: versionAuthor(ref.author)},
                versionAuthor(ref.author)),
        ))));

    const itemInspector = (sel) => {
        const item = sel.item;
        const usage = usageForItem(item);
        const usageCount = usage.deployments.length;
        return [
            div({class: "flex flex-none flex-col gap-2 border-b border-gray-800 py-2.5 pl-3 pr-9"},
                inspectorTitle(sel, item.name),
                div({class: "flex items-center gap-2"},
                    badge("Asset", "bg-teal-500/15 text-teal-300"),
                    inspectorSpaceTag(item.spaceId))),
            div({class: "app-scroll flex-1 min-h-0 overflow-y-auto px-3 py-2.5 flex flex-col gap-2.5"},
                dl({class: "m-0 grid grid-cols-[76px_1fr] items-baseline gap-x-2 gap-y-1.5"},
                    ...kvRow("Version", `v${item.version}`),
                    ...kvRow("Created", span({title: formatDateTime(item.createdAt, "")},
                        formatDate(item.createdAt, "-"),
                        span({class: "text-gray-500"}, ` · ${versionAuthor(item.meta.contentVersions?.[0]?.author)}`))),
                    ...kvRow("Size", `${fmtSize(item.sizeBytes)}${item.large ? " · large" : ""}`),
                    ...kvRow("In use by", usageCount
                        ? button({
                            type: "button",
                            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
                            onclick: () => { usageTarget.val = {resourceName: item.name, ...usage}; },
                        }, `${usageCount} deployment${usageCount === 1 ? "" : "s"}`)
                        : "0 deployments")),
                p({class: "mt-1 text-[10.5px] font-semibold uppercase tracking-wide text-gray-500"}, "Versions"),
                versionsList(item)),
            div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
                actionButton("Edit", () => openEditor(item), "bg-brand text-white hover:bg-blue-600"),
                actionButton("Upload version", () => pickUploadVersion(item)),
                actionButton("Rename", () => startRename(itemKey(item), item.name)),
                actionButton("Move", () => openMoveDialog(sel)),
                actionButton("Delete", () => openDelete(sel), "bg-gray-700 text-gray-200 hover:bg-red-600 hover:text-white")),
        ];
    };

    const groupStats = (sel) => {
        const items = currentItems();
        const inScope = sel.type === "space"
            ? items.filter((item) => item.spaceId === Number(sel.space.id))
            : items.filter((item) => {
                if (item.spaceId !== Number(sel.dir.spaceId)) return false;
                const byId = dirsById(currentDirs());
                const seen = new Set();
                let current = item.directoryId;
                while (current !== 0 && !seen.has(current)) {
                    if (current === Number(sel.dir.id)) return true;
                    seen.add(current);
                    current = Number(byId.get(current)?.parentId || 0);
                }
                return false;
            });
        const totalSize = inScope.reduce((sum, item) => sum + (item.sizeBytes || 0), 0);
        const newest = inScope.map((item) => item.createdAt).filter(Boolean).sort((a, b) => b - a)[0];
        return {inScope, totalSize, newest};
    };

    const groupInspector = (sel) => {
        const isSpace = sel.type === "space";
        const spaceId = isSpace ? Number(sel.space.id) : Number(sel.dir.spaceId);
        const stats = groupStats(sel);
        const folderCount = isSpace
            ? currentDirs().filter((d) => Number(d.spaceId) === spaceId).length
            : null;
        return [
            div({class: "flex flex-none flex-col gap-2 border-b border-gray-800 py-2.5 pl-3 pr-9"},
                inspectorTitle(sel, isSpace ? sel.space.name : sel.dir.name),
                div({class: "flex items-center gap-2"},
                    badge(isSpace ? "Space" : "Folder", "bg-slate-500/15 text-slate-300"),
                    isSpace ? "" : inspectorSpaceTag(spaceId))),
            div({class: "app-scroll flex-1 min-h-0 overflow-y-auto px-3 py-2.5 flex flex-col gap-2.5"},
                dl({class: "m-0 grid grid-cols-[76px_1fr] items-baseline gap-x-2 gap-y-1.5"},
                    ...kvRow("Contains", `${stats.inScope.length} asset${stats.inScope.length === 1 ? "" : "s"}`),
                    ...(folderCount !== null ? kvRow("Folders", String(folderCount)) : []),
                    ...kvRow("Size", fmtSize(stats.totalSize)),
                    ...kvRow("Newest", span({title: formatDateTime(stats.newest, "")}, formatDate(stats.newest, "-"))))),
            div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
                actionButton("New asset here", openCreate),
                actionButton("Upload here", pickUploadNew),
                ...(isSpace ? [actionButton("New folder here", openNewFolder)] : [
                    actionButton("Rename", () => startRename(selectedKey.val, sel.dir.name)),
                    actionButton("Move", () => openMoveDialog(sel)),
                    actionButton("Delete", () => openDelete(sel), "bg-gray-700 text-gray-200 hover:bg-red-600 hover:text-white"),
                ])),
        ];
    };

    const inspector = () => {
        const sel = resolveSelection();
        return div(
            {class: "relative flex flex-none flex-col border-l border-gray-700 bg-gray-950/35", style: () => `width:${inspectorWidth.val}px`},
            span({
                class: "vgrip",
                tabindex: "0",
                role: "separator",
                "aria-orientation": "vertical",
                "aria-label": "Resize details pane",
                onmousedown: startInspectorResize,
                onkeydown: (e) => {
                    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
                    e.preventDefault();
                    const step = e.shiftKey ? 48 : 16;
                    inspectorWidth.val = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN,
                        inspectorWidth.val + (e.key === "ArrowLeft" ? step : -step)));
                    persistView();
                },
            }),
            button({
                type: "button",
                "aria-label": "Close details",
                class: "absolute right-1.5 top-1.5 z-[6] inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
                onclick: () => { inspectorOpen.val = false; selectedKey.val = null; },
            }, closeIcon({class: "w-3.5 h-3.5"})),
            ...(sel
                ? (sel.type === "item" ? itemInspector(sel) : groupInspector(sel))
                : [div({class: "px-3 py-6 text-xs text-gray-500"}, "Select a row to see its details.")]),
        );
    };


    const errorLine = () => error.val ? div(
        {class: "flex flex-none items-center gap-3 border-b border-red-500/30 bg-red-500/10 px-3 py-1.5"},
        p({class: "min-w-0 flex-1 truncate text-xs text-red-300", title: error.val}, `Error: ${error.val}`),
        iconButton(closeIcon({class: "w-3 h-3"}), () => { error.val = null; }, {title: "Dismiss error", "aria-label": "Dismiss error"}),
    ) : "";

    const dialogShell = (labelledBy, ...children) => div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", onclick: () => { if (!dialogSaving.val) { folderDialog.val = null; moveDialog.val = null; deleteTarget.val = null; moveError.val = null; } }},
        div({
            class: "card w-full max-w-md flex flex-col gap-3 shadow-2xl",
            role: "dialog",
            "aria-modal": "true",
            "aria-labelledby": labelledBy,
            onclick: (e) => e.stopPropagation(),
        }, ...children),
    );

    const folderDialogEl = () => {
        if (!folderDialog.val) return "";
        return dialogShell("new-folder-title",
            h2({id: "new-folder-title", class: "text-base font-semibold"}, "New folder"),
            div({class: "flex min-w-0 items-center gap-2"},
                p({class: "shrink-0 text-xs text-gray-400"}, "In"),
                destinationPicker),
            input({
                class: "text-input font-mono text-sm",
                placeholder: "folder name",
                value: folderName,
                "aria-label": "Folder name",
                oninput: (e) => { folderName.val = e.target.value; },
                onkeydown: (e) => { if (e.key === "Enter") void createFolder(); },
            }),
            div({class: "flex items-center justify-end gap-2"},
                actionButton("Cancel", () => { if (!dialogSaving.val) folderDialog.val = null; }),
                spinnerButton("Create", createFolder,
                    "text-xs px-3 py-1.5 rounded-md font-medium bg-brand text-white hover:bg-blue-600", "button",
                    () => dialogSaving.val || !folderName.val.trim())));
    };

    const moveDialogEl = () => {
        const dialog = moveDialog.val;
        if (!dialog) return "";
        const currentId = dialog.currentId();
        const spacePicker = () => {
            if (!dialog.spaceId) return "";
            const spaces = selectEl({
                class: "input py-0.5 text-xs",
                "aria-label": "Destination space",
                onchange: (e) => { dialog.spaceId.val = Number(e.target.value); },
            }, ...listedSpaces().map((space) => option({value: String(space.id)}, space.name)));
            // Assigned after the options exist: a value set on an empty select is
            // discarded rather than remembered.
            spaces.value = String(dialog.spaceId.val);
            return div({class: "flex min-w-0 items-center gap-1.5"},
                p({class: "shrink-0 text-xs text-gray-400"}, "Space"),
                spaceDot(dialog.spaceId.val),
                spaces);
        };
        return dialogShell("move-title",
            h2({id: "move-title", class: "text-base font-semibold"}, dialog.label),
            spacePicker(),
            div({class: "app-scroll max-h-72 overflow-y-auto flex flex-col gap-0.5"},
                ...dialog.options().map((option) => button({
                    type: "button",
                    disabled: option.id === currentId,
                    class: `flex items-center gap-2 rounded px-2 py-1.5 text-left font-mono text-xs ${option.id === currentId
                        ? "text-gray-500"
                        : "text-gray-200 hover:bg-surface-hover cursor-pointer"}`,
                    onclick: () => { void applyMove(option.id); },
                },
                folderIcon({class: "w-3.5 h-3.5 flex-none text-slate-400"}),
                option.label,
                option.id === currentId ? span({class: "ml-auto font-sans text-[10px] text-gray-500"}, "current") : ""))),
            div({class: "flex items-center justify-end"},
                actionButton("Cancel", () => { if (!dialogSaving.val) moveDialog.val = null; })));
    };

    const deleteDialogEl = () => {
        const target = deleteTarget.val;
        if (!target) return "";
        return dialogShell("delete-title",
            h2({id: "delete-title", class: "text-base font-semibold"}, "Confirm delete"),
            p({class: "text-sm text-gray-300"}, `Are you sure you want to delete ${target.label}?`),
            div({class: "flex items-center justify-end gap-2"},
                actionButton("Cancel", () => { if (!dialogSaving.val) deleteTarget.val = null; }),
                spinnerButton("Confirm", confirmDelete,
                    "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500", "button",
                    () => dialogSaving.val)));
    };

    // A refused drop reports in a dialog rather than the inline error line: the
    // drag has already ended and the pointer is mid-table, so a banner at the
    // top of the page is easy to miss.
    const moveErrorEl = () => {
        const target = moveError.val;
        if (!target) return "";
        return dialogShell("move-error-title",
            h2({id: "move-error-title", class: "text-base font-semibold"}, "Move failed"),
            p({class: "text-sm text-gray-300"}, span({class: "font-mono text-gray-100"}, target.name), " was not moved."),
            p({class: "text-sm text-red-400"}, target.message),
            div({class: "flex items-center justify-end"},
                actionButton("Close", () => { moveError.val = null; }, "bg-brand text-white hover:bg-blue-600")));
    };

    const uploadDialogEl = () => {
        const target = uploadTarget.val;
        if (!target) return "";
        const pct = () => {
            const total = uploadTotal.val || 0;
            if (!total) return "0%";
            return `${Math.min(100, Math.round((uploadLoaded.val / total) * 100))}%`;
        };
        const progressText = () => {
            const total = uploadTotal.val || 0;
            const loaded = Math.min(uploadLoaded.val, total || uploadLoaded.val);
            return `Uploading ${target.file.name}: ${fmtSize(loaded)} / ${fmtSize(total)}`;
        };
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", onclick: closeUpload},
            div({
                class: "card w-full max-w-lg flex flex-col gap-4 shadow-2xl",
                role: "dialog",
                "aria-modal": "true",
                "aria-labelledby": "upload-asset-title",
                onclick: (e) => e.stopPropagation(),
            },
            h2({id: "upload-asset-title", class: "text-base font-semibold"},
                target.mode === "version" ? "Upload new version" : "Upload asset"),
            target.mode === "version"
                ? p({class: "text-xs text-gray-400"}, "Appends the next version of ",
                    span({class: "font-mono text-gray-300"}, target.key), ".")
                : p({class: "text-xs text-gray-400"}, "In ", span({class: "font-mono text-gray-300"}, target.location)),
            () => uploadedKey.val && !uploadSaving.val
                ? p({class: "text-sm font-medium text-green-300"}, `Upload successful. Size ${fmtSize(uploadTotal.val)}.`)
                : "",
            () => uploadSaving.val ? div({class: "flex flex-col gap-2"},
                p({class: "text-xs text-gray-400"}, progressText),
                div({class: "h-2 overflow-hidden rounded-full bg-gray-800"},
                    div({class: "h-full rounded-full bg-brand transition-all", style: () => `width:${pct()}`})),
            ) : "",
            () => !uploadedKey.val && !uploadSaving.val ? div({class: "flex flex-col gap-3"},
                p({class: "text-xs text-gray-400"}, `Selected ${target.file.name}, ${fmtSize(target.file.size)}.`),
                target.mode === "new" ? div({class: "flex flex-col gap-1"},
                    span({class: "text-xs text-gray-400"}, "Name"),
                    input({
                        class: "text-input font-mono",
                        value: uploadName,
                        "aria-label": "Uploaded asset name",
                        oninput: (e) => { uploadName.val = e.target.value; },
                    })) : "",
            ) : "",
            () => uploadedKey.val && !uploadSaving.val && target.mode === "new"
                ? div({class: "flex flex-col gap-1"},
                    span({class: "text-xs text-gray-400"}, "Name"),
                    div({class: "text-input font-mono text-gray-300"}, uploadedKey))
                : "",
            () => uploadError.val ? p({class: "text-sm text-red-400"}, uploadError) : "",
            () => !uploadedKey.val && !uploadSaving.val ? div({class: "flex items-center justify-end gap-2"},
                actionButton("Cancel", closeUpload),
                spinnerButton(uploadError.val ? "Retry upload" : "Upload", runUpload,
                    "text-xs px-3 py-1.5 rounded-md font-medium bg-brand text-white hover:bg-blue-600", "button",
                    () => uploadSaving.val || (target.mode === "new" && !uploadName.val.trim())),
            ) : "",
            () => uploadedKey.val && !uploadSaving.val ? div({class: "flex justify-end pt-1"},
                actionButton("Close", closeUpload),
            ) : "",
            ),
        );
    };

    const usageOverlayEl = () => {
        const target = usageTarget.val;
        if (!target) return "";
        return referenceUsageOverlay("asset", target.resourceName, target.deployments, target.settings,
            () => { usageTarget.val = null; });
    };

    const editorOverlayEl = () => {
        const target = editorTarget.val;
        if (!target) return "";
        return assetEditorOverlay({
            mode: target.mode,
            assetRef: target.mode === "create" ? null : {assetId: target.assetId, version: target.version},
            initialKey: "",
            latestVersion: target.latestVersion || 0,
            locationNode: target.mode === "create" ? destinationPicker : null,
            loadAsset: loadAssetPreview,
            // The picker owns the destination, so the space in the editor's
            // create request is overridden with whatever it is showing on save.
            createAsset: async (request) => {
                const {spaceId, directoryId} = createDest.val;
                const created = await capi.postV1AssetsCreate({...request, spaceId, assetDirectoryId: directoryId});
                expandTo(spaceId, directoryId);
                selectedKey.val = `asset:${created.id}`;
                return created;
            },
            saveVersion: (request) => capi.postV1AssetsSet(request),
            // The editor is a modal here, so a successful save closes it like
            // the secrets value overlay; the saved asset stays selected in the
            // tree and the inspector carries the update.
            onSaved: () => { editorTarget.val = null; },
            onClose: () => { editorTarget.val = null; },
        });
    };


    return div(
        // bg-surface: per the design mock the explorer is one flush card
        // surface, not content floating on the page background.
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        toolbar(),
        errorLine,
        div({class: "flex flex-1 min-h-0"},
            div({class: "flex min-w-0 flex-1 flex-col"},
                tableArea,
                pathbar),
            // The inspector only exists alongside a selection: an empty page
            // gets the full table width instead of a "select a row" stub.
            () => inspectorOpen.val && selectedKey.val ? inspector() : ""),
        () => openMenu.val ? div({class: "fixed inset-0 z-20", onclick: () => { openMenu.val = null; }}) : "",
        uploadPicker,
        folderDialogEl,
        moveDialogEl,
        deleteDialogEl,
        moveErrorEl,
        uploadDialogEl,
        usageOverlayEl,
        editorOverlayEl,
    );
}
