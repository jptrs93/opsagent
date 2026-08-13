import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {referenceUsageOverlay} from "../components/referenceUsageOverlay.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {valueOverlay} from "../components/valueOverlay.js";
import {formatDate, formatDateTime} from "../lib/date.js";
import {containerWorkload} from "../lib/deploymentConfig.js";
import {
    caretRightIcon, checkIcon, chevronDownIcon, closeIcon, columnsIcon, configSlidersIcon,
    copyIcon, editIcon, eyeOpenIcon, folderIcon, plusIcon, searchIcon, secretKeyIcon,
    sortArrowIcon,
} from "../lib/icons.js";
import {selectableSpaces} from "../lib/nodeSpaces.js";
import {deploymentUsages, deploymentUsesEnvReferences} from "../lib/referenceUsage.js";
import {
    ALL_COLUMNS, DEFAULT_COLUMNS, DEFAULT_COLUMN_WIDTHS, DEFAULT_TYPES,
    buildRows, checkDrop, dirsById, dirPathSegments, dragSource, dropDestination,
    emptySpaceIds, flexColumnKey, folderOptions, itemKey, itemPathSegments, makeItems,
    sameSet, spaceHue,
} from "../lib/valueExplorer.js";
import {
    deploymentsS, machinesS, primaryConfigS, secretMetasS, secretsStatusS, spacesS,
    userConfigsS, usersMapS, valueDirectoriesS,
} from "../state/deployments.js";

// selectEl: the pages define their own row-selection helper named `select`.
const {button, col, colgroup, dd, div, dl, dt, h2, input, option, p, select: selectEl, span, table, tbody, td, th, thead, tr} = van.tags;

const SECRET_MASK = "••••••••••••";
const VIEW_STORAGE_KEY = "opendeploySecretsExplorerView";
const INSPECTOR_MIN = 220;
const INSPECTOR_MAX = 460;

const loadView = () => {
    try {
        return JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY)) || {};
    } catch (_) {
        return {};
    }
};

// Settings references, labelled for the usage overlay. These mirror the typed
// secret/config refs the primary configuration can pin.
const settingConfigRefs = (settings) => [
    ["Web UI HTTP enabled", settings?.httpWeb?.enabled?.configRef?.versionId],
    ["Web UI HTTP listen", settings?.httpWeb?.listen?.configRef?.versionId],
    ["Web UI HTTPS enabled", settings?.httpsWeb?.enabled?.configRef?.versionId],
    ["Web UI HTTPS listen", settings?.httpsWeb?.listen?.configRef?.versionId],
    ["Web UI use self managed TLS cert", settings?.httpsWeb?.tlsSelfManaged?.configRef?.versionId],
    ["Web UI ACME hosts", settings?.httpsWeb?.acmeHosts?.configRef?.versionId],
    ["Web UI ACME email", settings?.httpsWeb?.acmeEmail?.configRef?.versionId],
    ["Cluster listen", settings?.cluster?.listen?.configRef?.versionId],
    ["Cluster enrollment listen", settings?.cluster?.enrollmentListen?.configRef?.versionId],
    ["Backup enabled", settings?.backup?.enabled?.configRef?.versionId],
    ["Backup S3 access key ID", settings?.backup?.s3AccessKeyId?.configRef?.versionId],
    ["Backup S3 bucket", settings?.backup?.s3Bucket?.configRef?.versionId],
    ["Backup S3 path", settings?.backup?.s3Path?.configRef?.versionId],
    ["Backup S3 region", settings?.backup?.s3Region?.configRef?.versionId],
    ["Backup S3 endpoint", settings?.backup?.s3Endpoint?.configRef?.versionId],
    ["Use separate large assets S3", settings?.largeAssets?.useSeparateS3?.configRef?.versionId],
    ["Large asset S3 access key ID", settings?.largeAssets?.s3AccessKeyId?.configRef?.versionId],
    ["Large asset S3 bucket", settings?.largeAssets?.s3Bucket?.configRef?.versionId],
    ["Large asset S3 path", settings?.largeAssets?.s3Path?.configRef?.versionId],
    ["Large asset S3 region", settings?.largeAssets?.s3Region?.configRef?.versionId],
    ["Large asset S3 endpoint", settings?.largeAssets?.s3Endpoint?.configRef?.versionId],
].map(([label, id]) => ({label, id: Number(id || 0)})).filter((ref) => ref.id);

const settingSecretRefs = (settings) => [
    ["Web UI TLS cert PEM", settings?.httpsWeb?.tlsCertPem?.versionId],
    ["GitHub token", settings?.repo?.githubToken?.versionId],
    ["Backup S3 secret access key", settings?.backup?.s3SecretAccessKey?.versionId],
    ["Large asset S3 secret access key", settings?.largeAssets?.s3SecretAccessKey?.versionId],
].map(([label, id]) => ({label, id: Number(id || 0)})).filter((ref) => ref.id);

export function secretsPage() {
    const saved = loadView();

    // The two halves of the space filter. hiddenSpaces is what was hidden by
    // hand and persists; shownEmptySpaces re-admits a space the empty-space
    // default hid, and is deliberately per-visit — landing on the page starts
    // from the default again.
    const hiddenSpaces = van.state(new Set(Array.isArray(saved.hiddenSpaces) ? saved.hiddenSpaces : []));
    const shownEmptySpaces = van.state(new Set());
    const types = van.state(new Set(Array.isArray(saved.types) ? saved.types : DEFAULT_TYPES));
    const shownCols = van.state(new Set(Array.isArray(saved.cols) ? saved.cols : DEFAULT_COLUMNS));
    const colWidths = van.state({...DEFAULT_COLUMN_WIDTHS, ...(saved.colWidths || {})});
    const sort = van.state(saved.sort?.key ? saved.sort : {key: "name", dir: "asc"});
    const expanded = van.state(new Set(Array.isArray(saved.expanded) ? saved.expanded : []));
    const inspectorWidth = van.state(Number(saved.inspectorWidth) || 280);
    const inspectorOpen = van.state(true);
    const selectedKey = van.state(null);
    const search = van.state("");
    const openMenu = van.state(null); // "spaces" | "types" | "cols" | null
    const error = van.state(null);
    const revealed = van.state(null); // {key, value}
    // renameState marks which row is being renamed; the draft lives in its own
    // state that no binding reads, so typing never rebuilds the input.
    const renameState = van.state(null); // {key}
    const renameDraft = van.state("");
    const folderName = van.state("");
    const copiedKey = van.state("");

    const usageTarget = van.state(null);
    const valueTarget = van.state(null);   // {item, originalValue, referencingDeployments}
    const createTarget = van.state(null);  // {type}
    const folderDialog = van.state(null);  // truthy while the new-folder dialog is open
    // Destination of whichever create dialog is open. It lives outside those
    // dialogs' own state so that changing the space re-renders only the picker:
    // rebuilding the dialog would discard a half-typed name or value.
    const createDest = van.state({spaceId: 0, directoryId: 0});
    const moveDialog = van.state(null);    // {label, options(), currentId(), apply, spaceId?: van.state}
    const moveError = van.state(null);     // {name, message} from a refused drop
    const deleteTarget = van.state(null);  // {label, apply}
    const dialogSaving = van.state(false);

    let expandedTouched = Array.isArray(saved.expanded);

    const persistView = () => {
        try {
            localStorage.setItem(VIEW_STORAGE_KEY, JSON.stringify({
                hiddenSpaces: [...hiddenSpaces.val],
                types: [...types.val],
                cols: [...shownCols.val],
                colWidths: colWidths.val,
                sort: sort.val,
                expanded: [...expanded.val],
                inspectorWidth: inspectorWidth.val,
            }));
        } catch (_) { /* view state is a convenience, never load-bearing */ }
    };

    // ---- derived data -----------------------------------------------------

    const secretsUnlocked = () => secretsStatusS.val?.unlocked === true;
    const currentItems = () => makeItems(secretMetasS.val, userConfigsS.val, secretsUnlocked());
    const currentDirs = () => valueDirectoriesS.val || [];
    // listedSpaces is the page's whole notion of "the spaces": the opendeploy
    // space is dropped once here so it stays out of the tree, the filter menu
    // and every destination picker without each of them re-testing for it.
    const listedSpaces = () => selectableSpaces(spacesS.val);
    const spaceName = (id) => (spacesS.val || []).find((s) => Number(s.id) === Number(id))?.name || `space ${id}`;

    // Derived, not latched at mount: a space that gains its first value — or
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
    const typesDirty = () => !sameSet(types.val, new Set(DEFAULT_TYPES));
    const colsDirty = () => !sameSet(shownCols.val, new Set(DEFAULT_COLUMNS));

    const usageForItem = (item) => {
        const refIds = new Set((item.meta.versionRefs || []).map((ref) => Number(ref.id)));
        const settings = (item.kind === "secret"
            ? settingSecretRefs(primaryConfigS.val?.config?.settings)
            : settingConfigRefs(primaryConfigS.val?.config?.settings)
        ).filter((ref) => refIds.has(ref.id));
        const referenceKey = item.kind === "secret" ? "secretVersionId" : "configVersionId";
        const deployments = deploymentUsages(deploymentsS.val, spacesS.val, machinesS.val, (deployment) => {
            const cfg = deployment?.config;
            if (!cfg || cfg.deleted) return false;
            const envVars = containerWorkload(cfg)?.runtime?.envVars || {};
            return Object.values(envVars).some((value) => refIds.has(Number(value?.[referenceKey] || 0)));
        });
        return {deployments, settings};
    };

    const referencingDeploymentVersions = (item) => {
        const refIds = new Set((item.meta.versionRefs || []).map((ref) => Number(ref.id)));
        return (deploymentsS.val || []).map((deployment) => deployment?.config).filter((cfg) =>
            cfg && !cfg.deleted && deploymentUsesEnvReferences(cfg, item.kind, refIds),
        ).map((cfg) => ({id: cfg.id, version: cfg.version}));
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
    // disclosure. Also drops a selection stranded by the space or type filter —
    // but only when the row demonstrably exists and is filtered out: a key the
    // stream has not echoed yet (a create still in flight) must survive.
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
        if (item && (filteredSpaces.val.has(item.spaceId) || !types.val.has(item.kind))) selectedKey.val = null;
    });

    // ---- actions ----------------------------------------------------------

    const select = (key) => {
        selectedKey.val = key;
        inspectorOpen.val = true;
        revealed.val = null;
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

    const secretLatestValue = async (item) => {
        const res = await capi.postV1SecretsReveal({id: item.meta.versionRefs[0].id});
        return new TextDecoder().decode(res.value);
    };

    const revealItem = async (item) => {
        select(itemKey(item));
        try {
            error.val = null;
            revealed.val = {key: itemKey(item), value: item.kind === "secret" ? await secretLatestValue(item) : item.value};
        } catch (e) {
            error.val = e.message;
        }
    };

    const copyItemValue = async (item) => {
        try {
            error.val = null;
            const value = item.kind === "secret" ? await secretLatestValue(item) : item.value;
            await navigator.clipboard.writeText(value);
            copiedKey.val = itemKey(item);
            setTimeout(() => { if (copiedKey.val === itemKey(item)) copiedKey.val = ""; }, 1500);
        } catch (e) {
            error.val = e.message;
        }
    };

    const editItem = async (item) => {
        try {
            error.val = null;
            const originalValue = item.kind === "secret" ? await secretLatestValue(item) : item.value;
            valueTarget.val = {item, originalValue, referencingDeployments: referencingDeploymentVersions(item)};
        } catch (e) {
            error.val = e.message;
        }
    };

    const saveItemValue = async (item, value, {updateReferencedDeployments = false, referencingDeployments = []} = {}) => {
        if (item.kind === "secret") {
            await capi.postV1SecretsSet({
                secretId: item.id,
                value: new TextEncoder().encode(value),
                updateReferencingDeployments: updateReferencedDeployments,
                referencingDeployments: updateReferencedDeployments ? referencingDeployments : [],
            });
        } else {
            await capi.postV1ConfigsSet({
                configId: item.id,
                value,
                updateReferencingDeployments: updateReferencedDeployments,
                referencingDeployments: updateReferencedDeployments ? referencingDeployments : [],
            });
        }
        revealed.val = null;
    };

    // Creating starts in the selection's folder: a selected folder itself, a
    // selected item's folder, or a selected space's root. The dialog's picker
    // can move it elsewhere from there.
    const createContext = () => {
        const sel = resolveSelection();
        if (sel?.type === "space") return {spaceId: Number(sel.space.id), directoryId: 0};
        if (sel?.type === "dir") return {spaceId: Number(sel.dir.spaceId), directoryId: Number(sel.dir.id)};
        if (sel?.type === "item") return {spaceId: sel.item.spaceId, directoryId: sel.item.directoryId};
        const first = visibleSpaces()[0] || listedSpaces()[0];
        return {spaceId: first ? Number(first.id) : 1, directoryId: 0};
    };

    const openCreate = (type) => {
        createDest.val = createContext();
        createTarget.val = {type};
    };

    const createResource = async (type, value, name) => {
        const {spaceId, directoryId} = createDest.val;
        error.val = null;
        const meta = type === "secret"
            ? await capi.postV1SecretsCreate({name, value: new TextEncoder().encode(value), spaceId, valueDirectoryId: directoryId})
            : await capi.postV1ConfigsCreate({name, value, spaceId, valueDirectoryId: directoryId});
        // A create while the filter hides its type would vanish on save, so the
        // filter opens back up and the tree walks to the new row.
        if (!types.val.has(type)) {
            types.val = new Set([...types.val, type]);
            persistView();
        }
        expandTo(spaceId, directoryId);
        selectedKey.val = `${type}:${meta.id}`;
    };

    const openNewFolder = () => {
        createDest.val = createContext();
        folderName.val = "";
        folderDialog.val = true;
    };

    const createFolder = async () => {
        const {spaceId, directoryId} = createDest.val;
        const name = folderName.val.trim();
        if (!folderDialog.val || !name || dialogSaving.val) return;
        dialogSaving.val = true;
        try {
            error.val = null;
            const dir = await capi.postV1ValueDirectoriesCreate({spaceId, parentId: directoryId, name});
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
                await capi.postV1ValueDirectoriesRename({directoryId: Number(sel.dir.id), newName: name});
            } else if (sel.type === "item" && sel.item.kind === "secret") {
                await capi.postV1SecretsRename({secretId: sel.item.id, newName: name});
            } else if (sel.type === "item") {
                await capi.postV1ConfigsRename({configId: sel.item.id, newName: name});
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
                    await capi.postV1ValueDirectoriesMove({directoryId: Number(dir.id), newParentId: destination});
                    expandTo(Number(dir.spaceId), destination);
                },
            };
            return;
        }
        // Items can change space: the picker swaps the folder list to the chosen
        // space's tree, and a changed space rides the move request. The server
        // refuses if anything outside the destination still references the item.
        const item = sel.item;
        const spaceId = van.state(Number(item.spaceId));
        moveDialog.val = {
            label: `Move ${item.name}`,
            spaceId,
            options: () => folderOptions(currentDirs(), spaceId.val),
            currentId: () => (spaceId.val === Number(item.spaceId) ? item.directoryId : null),
            apply: async (destination) => {
                const request = {valueDirectoryId: destination};
                if (spaceId.val !== Number(item.spaceId)) request.spaceId = spaceId.val;
                if (item.kind === "secret") await capi.postV1SecretsMove({secretId: item.id, ...request});
                else await capi.postV1ConfigsMove({configId: item.id, ...request});
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
                    await capi.postV1ValueDirectoriesDelete({directoryId: Number(sel.dir.id)});
                    selectedKey.val = null;
                },
            };
            return;
        }
        const item = sel.item;
        deleteTarget.val = {
            label: `${item.kind} ${item.name}`,
            apply: async () => {
                if (item.kind === "secret") await capi.postV1SecretsDelete({secretId: item.id});
                else await capi.postV1ConfigsDelete({configId: item.id});
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

    // ---- resize wiring ----------------------------------------------------

    const startColResize = (event, colKey, min) => {
        event.preventDefault();
        event.stopPropagation();
        const colEl = event.target.closest("table")?.querySelector(`col[data-col="${colKey}"]`);
        const startX = event.clientX;
        const startW = colWidths.val[colKey] ?? DEFAULT_COLUMN_WIDTHS[colKey];
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
        const current = colWidths.val[colKey] ?? DEFAULT_COLUMN_WIDTHS[colKey];
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

    // ---- small building blocks --------------------------------------------

    const spaceDot = (spaceId) => span({
        class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
        style: `background:${spaceHue(spaceId)}`,
    });

    const typeIcon = (kind, extra = "") => kind === "secret"
        ? secretKeyIcon({class: `w-[13px] h-[13px] flex-none text-purple-300 ${extra}`, role: "img", "aria-label": "Secret"})
        : configSlidersIcon({class: `w-[13px] h-[13px] flex-none text-blue-300 ${extra}`, role: "img", "aria-label": "Config"});

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

    // ---- toolbar ----------------------------------------------------------

    // label is a function so the button face stays live (space dots, dimmed
    // type icons) without rebuilding the toolbar and losing search focus.
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

    const typesMenu = () => {
        const counts = new Map();
        for (const item of currentItems()) {
            if (!filteredSpaces.val.has(item.spaceId)) counts.set(item.kind, (counts.get(item.kind) || 0) + 1);
        }
        return menuShell(
            menuHeader("Type"),
            ...DEFAULT_TYPES.map((kind) => menuRow(() => {
                const next = new Set(types.val);
                next.has(kind) ? next.delete(kind) : next.add(kind);
                types.val = next;
                persistView();
            },
            menuCheck(types.val.has(kind)),
            typeIcon(kind),
            kind === "secret" ? "Secrets" : "Configs",
            menuTail(String(counts.get(kind) || 0)),
            )),
            ...(typesDirty() ? resetRow(() => {
                types.val = new Set(DEFAULT_TYPES);
                persistView();
            }) : []),
        );
    };

    const colsMenu = () => menuShell(
        menuHeader("Columns"),
        button({
            type: "button", disabled: true,
            class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-500",
        }, menuCheck(true), "Name", menuTail("always")),
        ...ALL_COLUMNS.filter((c) => c.key !== "name").map((c) => menuRow(() => {
            const next = new Set(shownCols.val);
            next.has(c.key) ? next.delete(c.key) : next.add(c.key);
            shownCols.val = next;
            persistView();
        }, menuCheck(shownCols.val.has(c.key)), c.label || "Actions")),
        ...(colsDirty() ? resetRow(() => {
            shownCols.val = new Set(DEFAULT_COLUMNS);
            persistView();
        }) : []),
    );

    const typesButtonLabel = () => {
        if (types.val.size === 0) return "No types";
        if (types.val.size === DEFAULT_TYPES.length) return "All types";
        return types.val.has("secret") ? "Secrets" : "Configs";
    };

    const toolbar = () => div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        div({class: "relative"},
            searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
            input({
                class: "text-input search-input search-input-iconed",
                type: "search",
                placeholder: "Search secrets / configs",
                "aria-label": "Search secrets and configs",
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
                menu: "types",
                dirty: typesDirty,
                ariaLabel: "Filter types",
                label: () => [
                    typeIcon("secret", types.val.has("secret") ? "" : "opacity-25"),
                    typeIcon("config", types.val.has("config") ? "" : "opacity-25"),
                    typesButtonLabel(),
                    chevronDownIcon({class: "w-3 h-3"}),
                ],
            }),
            () => openMenu.val === "types" ? typesMenu() : ""),
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
            class: "flex items-center gap-1 whitespace-nowrap rounded-lg bg-gray-700 px-3 py-1.5 text-sm text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
            onclick: () => openCreate("config"),
        }, plusIcon(), "New config"),
        button({
            type: "button",
            disabled: () => !secretsUnlocked(),
            class: () => `flex items-center gap-1 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm transition-colors ${secretsUnlocked()
                ? "bg-brand text-white hover:bg-blue-600 cursor-pointer"
                : "bg-gray-700 text-gray-400 opacity-50 cursor-not-allowed"}`,
            onclick: () => { if (secretsUnlocked()) openCreate("secret"); },
        }, plusIcon(), "New secret"),
    );

    // ---- table ------------------------------------------------------------

    const activeColumns = () => ALL_COLUMNS.filter((c) => shownCols.val.has(c.key));

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

    // ---- drag and drop ----------------------------------------------------
    //
    // Mirrors the assets explorer. The drag bookkeeping is plain variables
    // rather than van states on purpose: the table is one derived node, so a
    // state read here would rebuild every row on each dragover — hundreds of
    // times per drag. The hover affordance is written straight onto the row
    // element for the same reason.
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
                await capi.postV1ValueDirectoriesMove({
                    directoryId: drag.id, newParentId: destination.directoryId, spaceId: destination.spaceId,
                });
            } else if (drag.kind === "secret") {
                await capi.postV1SecretsMove({
                    secretId: drag.id, valueDirectoryId: destination.directoryId, spaceId: destination.spaceId,
                });
            } else {
                await capi.postV1ConfigsMove({
                    configId: drag.id, valueDirectoryId: destination.directoryId, spaceId: destination.spaceId,
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
            iconButton(eyeOpenIcon({class: "w-3.5 h-3.5"}), () => { void revealItem(item); }, {
                title: `${item.kind === "secret" ? "Reveal" : "View"} ${item.name}`,
                "aria-label": `${item.kind === "secret" ? "Reveal" : "View"} ${item.name}`,
            }),
            iconButton(() => copiedKey.val === itemKey(item)
                ? checkIcon({class: "w-3.5 h-3.5 text-green-400"})
                : copyIcon({class: "w-3.5 h-3.5"}), () => { void copyItemValue(item); }, {
                title: `Copy ${item.name}`,
                "aria-label": `Copy ${item.name}`,
            }),
            iconButton(editIcon({class: "w-3.5 h-3.5"}), () => { void editItem(item); }, {
                title: `Edit ${item.name}`,
                "aria-label": `Edit ${item.name}`,
            })),
    );

    const usesCell = (item, usesMap) => {
        const count = usesMap.get(itemKey(item)) || 0;
        if (!count) return "0";
        return button({
            type: "button",
            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
            "aria-label": `Show usage for ${item.kind} ${item.name}`,
            onclick: (e) => {
                e.stopPropagation();
                usageTarget.val = {resourceType: item.kind, resourceName: item.name, ...usageForItem(item)};
            },
        }, String(count));
    };

    const itemCell = (column, item, usesMap) => {
        const base = "border-b border-gray-800/80 px-2 py-1 whitespace-nowrap overflow-hidden text-gray-400";
        if (column.key === "version") return td({class: `${base} text-right tabular-nums`}, `v${item.version}`);
        if (column.key === "created") return td({class: base, title: formatDateTime(item.createdAt, "")}, formatDate(item.createdAt, "-"));
        if (column.key === "uses") return td({class: `${base} text-right tabular-nums`}, usesCell(item, usesMap));
        if (column.key === "value") {
            return td({class: `${base} font-mono text-ellipsis`, title: item.kind === "config" ? item.value : "Secret value"},
                item.kind === "secret" ? span({class: "text-gray-600 tracking-widest"}, SECRET_MASK) : item.value);
        }
        if (column.key === "actions") return itemActionsCell(item);
        return td({class: base});
    };

    const itemRow = (row, columns, usesMap) => tr(
        {class: rowClass(row), onclick: () => select(row.key), ...dndProps(row)},
        td({class: "border-b border-gray-800/80 py-1 pr-2 font-mono text-[13px] whitespace-nowrap overflow-hidden", style: namePad(row.depth)},
            span({class: "flex items-center gap-1.5 min-w-0"},
                span({class: "w-4 flex-none"}),
                typeIcon(row.item.kind),
                nameText(row.item.name))),
        ...columns.slice(1).map((column) => itemCell(column, row.item, usesMap)),
    );

    const emptyState = (text) => div({class: "flex-1 min-h-0 p-6 text-sm text-gray-500"}, text);

    const tableArea = () => {
        if (types.val.size === 0) {
            return emptyState("Neither secrets nor configs are shown. Turn one back on from the Type filter.");
        }
        const spaces = visibleSpaces();
        if (!spaces.length) {
            return emptyState("No spaces shown. Add one from the Spaces filter.");
        }
        const items = currentItems();
        const usesMap = new Map(items.map((item) => {
            const usage = usageForItem(item);
            return [itemKey(item), usage.deployments.length + usage.settings.length];
        }));
        const {rows} = buildRows({
            spaces: listedSpaces(),
            dirs: currentDirs(),
            items,
            hiddenSpaceIds: filteredSpaces.val,
            types: types.val,
            query: search.val,
            expanded: expanded.val,
            sort: sort.val,
            usesByKey: usesMap,
        });
        if (search.val.trim() && rows.every((row) => row.type === "space" && row.count === 0)) {
            return emptyState("Nothing matches your search.");
        }
        const columns = activeColumns();
        const flexKey = flexColumnKey(shownCols.val);
        const lastKey = columns.length ? columns[columns.length - 1].key : null;
        return div(
            {class: "app-scroll flex-1 min-h-0 overflow-auto"},
            table(
                {class: "w-full table-fixed border-separate border-spacing-0 text-sm"},
                colgroup(...columns.map((c) => c.key === flexKey
                    ? col({"data-col": c.key})
                    : col({"data-col": c.key, style: `width:${colWidths.val[c.key] ?? DEFAULT_COLUMN_WIDTHS[c.key]}px`}))),
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

    // ---- path bar ---------------------------------------------------------

    const hiddenByTypeCount = () => {
        let hidden = 0;
        for (const item of currentItems()) {
            if (!filteredSpaces.val.has(item.spaceId) && !types.val.has(item.kind)) hidden += 1;
        }
        return hidden;
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
        // No selection, nothing filtered: no bar at all. The bar stays for a
        // bare hidden-by-type count, since that is what keeps the type filter
        // legible when rows are missing.
        const hidden = hiddenByTypeCount();
        if (!parts.length && !hidden) return "";
        return div(
            {
                class: "flex flex-none items-center gap-1.5 border-t border-gray-700 bg-gray-950/40 px-3 py-1.5 font-mono text-[11px] text-gray-500",
                "data-testid": "explorer-pathbar",
            },
            ...parts.flatMap((part, i) => [
                i === 0 ? spaceDot(part.spaceId) : span({class: "opacity-60"}, "/"),
                span({class: i === parts.length - 1 ? "text-gray-300 font-medium" : ""}, part.text),
            ]),
            hidden ? span({class: "ml-auto font-sans tracking-wide"}, `${hidden} hidden by type filter`) : "",
        );
    };

    // ---- inspector --------------------------------------------------------

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
    const versionAuthor = (id) => usersMapS.val.get(Number(id))?.name || "unknown";

    // table-fixed and the colgroup are what make the columns line up: under
    // automatic layout a browser sizes columns from their content and ignores
    // max-width on cells, so widths drifted with whatever name a row happened to
    // carry, and the author cell never truncated. Fixed layout takes the widths
    // from here instead, leaving the author column the slack. The newest version
    // is marked by its version number rather than by a "current" column that
    // would be empty on every other row. Matches the assets inspector.
    const versionsList = (meta) => table({class: "w-full table-fixed font-mono text-[11px] text-gray-400"},
        // Sized to the widest real string in each column at 11px mono, plus the
        // pr-2 gutter: "v123", "Sep 30, 2026". The author takes the rest.
        colgroup(col({style: "width:2.1rem"}), col({style: "width:5.7rem"}), col()),
        tbody(...(meta.versionRefs || []).map((ref, i) => tr(
            td({
                class: `truncate py-0.5 pr-2 font-medium ${i === 0 ? "text-green-400" : "text-gray-200"}`,
                title: i === 0 ? "Current version" : "",
            }, `v${ref.version}`),
            td({class: "truncate py-0.5 pr-2", title: formatDateTime(ref.createdAt, "")},
                formatDate(ref.createdAt, "-")),
            td({class: "truncate py-0.5 text-gray-500", title: versionAuthor(ref.createdBy)},
                versionAuthor(ref.createdBy)),
        ))));

    const inspectorValue = (item) => {
        const shown = revealed.val?.key === itemKey(item) ? revealed.val.value : null;
        if (item.kind === "config") {
            return div({class: "max-h-24 overflow-y-auto app-scroll break-all font-mono text-xs text-gray-300"}, item.value);
        }
        if (shown !== null) {
            return div({class: "flex items-start gap-1.5 min-w-0"},
                div({class: "max-h-24 overflow-y-auto app-scroll break-all font-mono text-xs text-gray-300 min-w-0"}, shown),
                iconButton(closeIcon({class: "w-3 h-3"}), () => { revealed.val = null; }, {title: "Hide value", "aria-label": "Hide value"}));
        }
        return div({class: "flex items-center gap-1.5"},
            span({class: "font-mono text-xs tracking-widest text-gray-600"}, SECRET_MASK),
            iconButton(eyeOpenIcon({class: "w-3.5 h-3.5"}), () => { void revealItem(item); }, {
                title: "Reveal value", "aria-label": "Reveal value",
                class: secretsUnlocked() ? "" : "opacity-40 pointer-events-none",
            }));
    };

    const itemInspector = (sel) => {
        const item = sel.item;
        const usage = usageForItem(item);
        const usageCount = usage.deployments.length + usage.settings.length;
        return [
            div({class: "flex flex-none flex-col gap-2 border-b border-gray-800 py-2.5 pl-3 pr-9"},
                inspectorTitle(sel, item.name),
                div({class: "flex items-center gap-2"},
                    badge(item.kind === "secret" ? "Secret" : "Config",
                        item.kind === "secret" ? "bg-purple-500/15 text-purple-300" : "bg-blue-500/15 text-blue-300"),
                    inspectorSpaceTag(item.spaceId))),
            div({class: "app-scroll flex-1 min-h-0 overflow-y-auto px-3 py-2.5 flex flex-col gap-2.5"},
                dl({class: "m-0 grid grid-cols-[76px_1fr] items-baseline gap-x-2 gap-y-1.5"},
                    ...kvRow("Version", `v${item.version}`),
                    ...kvRow("Created", span({title: formatDateTime(item.createdAt, "")}, formatDate(item.createdAt, "-"))),
                    ...kvRow("In use by", usageCount
                        ? button({
                            type: "button",
                            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
                            onclick: () => { usageTarget.val = {resourceType: item.kind, resourceName: item.name, ...usage}; },
                        }, `${usageCount} reference${usageCount === 1 ? "" : "s"}`)
                        : "0 references"),
                    ...kvRow("Value", inspectorValue(item))),
                p({class: "mt-1 text-[10.5px] font-semibold uppercase tracking-wide text-gray-500"}, "Versions"),
                versionsList(item.meta)),
            div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
                actionButton("Edit", () => editItem(item), "bg-brand text-white hover:bg-blue-600"),
                // Secret copies round-trip to the reveal endpoint, so the label
                // acknowledges the write the same way the row's copy icon does.
                actionButton(() => copiedKey.val === itemKey(item)
                    ? span({class: "text-green-400"}, "Copied")
                    : "Copy", () => copyItemValue(item)),
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
        const shown = inScope.filter((item) => types.val.has(item.kind));
        const secrets = inScope.filter((item) => item.kind === "secret").length;
        const newest = inScope.map((item) => item.createdAt).filter(Boolean).sort((a, b) => b - a)[0];
        return {inScope, shown, secrets, newest};
    };

    const groupInspector = (sel) => {
        const isSpace = sel.type === "space";
        const spaceId = isSpace ? Number(sel.space.id) : Number(sel.dir.spaceId);
        const stats = groupStats(sel);
        const hidden = stats.inScope.length - stats.shown.length;
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
                    ...kvRow("Contains", `${stats.inScope.length} item${stats.inScope.length === 1 ? "" : "s"}${hidden ? ` · ${stats.shown.length} shown` : ""}`),
                    ...kvRow("Secrets", String(stats.secrets)),
                    ...kvRow("Configs", String(stats.inScope.length - stats.secrets)),
                    ...(folderCount !== null ? kvRow("Folders", String(folderCount)) : []),
                    ...kvRow("Newest", span({title: formatDateTime(stats.newest, "")}, formatDate(stats.newest, "-"))))),
            div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
                actionButton("New secret here", () => { if (secretsUnlocked()) openCreate("secret"); }, "bg-gray-700 text-gray-200 hover:bg-gray-600", {disabledWhen: () => !secretsUnlocked()}),
                actionButton("New config here", () => openCreate("config")),
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

    // ---- banners and dialogs ----------------------------------------------

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

    const lockedBanner = () => secretsStatusS.val && !secretsStatusS.val.unlocked ? div(
        {class: "flex flex-none flex-wrap items-center gap-3 border-b border-amber-500/30 bg-amber-500/10 px-3 py-2"},
        p({class: "text-sm font-semibold text-amber-300"}, "Secrets store is locked"),
        p({class: "text-xs text-gray-400"}, "Configs remain available. Enter the recovery code to unlock secret listing, editing, and reveal."),
        div({class: "flex items-center gap-2"},
            input({
                class: "input font-mono text-xs w-56",
                type: "text",
                placeholder: "recovery code",
                value: unlockCode,
                oninput: (e) => { unlockCode.val = e.target.value; },
            }),
            spinnerButton("Unlock", unlock, "text-xs px-3 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600", "button",
                () => !unlockCode.val.trim())),
    ) : "";

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

    const usageOverlayEl = () => {
        const target = usageTarget.val;
        if (!target) return "";
        return referenceUsageOverlay(target.resourceType, target.resourceName, target.deployments, target.settings,
            () => { usageTarget.val = null; });
    };

    const valueOverlayEl = () => {
        const target = valueTarget.val;
        if (!target) return "";
        const {item, originalValue, referencingDeployments} = target;
        return valueOverlay({
            name: item.name,
            type: item.kind,
            value: originalValue,
            version: item.version,
            createdAt: item.createdAt,
            deploymentCount: referencingDeployments.length,
            onSave: (value, _name, options) => saveItemValue(item, value, {...options, referencingDeployments}),
            onClose: () => { valueTarget.val = null; },
        });
    };

    const createOverlayEl = () => {
        const target = createTarget.val;
        if (!target) return "";
        return valueOverlay({
            mode: "create",
            type: target.type,
            locationNode: destinationPicker,
            onSave: (value, name) => createResource(target.type, value, name),
            onClose: () => { createTarget.val = null; },
        });
    };

    // ---- page -------------------------------------------------------------

    return div(
        // bg-surface: per the design mock the explorer is one flush card
        // surface, not content floating on the page background.
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        lockedBanner,
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
        folderDialogEl,
        moveDialogEl,
        deleteDialogEl,
        moveErrorEl,
        usageOverlayEl,
        valueOverlayEl,
        createOverlayEl,
    );
}
