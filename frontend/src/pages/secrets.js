import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {referenceUsageOverlay} from "../components/referenceUsageOverlay.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {valueOverlay} from "../components/valueOverlay.js";
import {formatDateTime} from "../lib/date.js";
import {checkIcon, copyIcon, editIcon, expandIcon, eyeOpenIcon, plusIcon, trashIcon} from "../lib/icons.js";
import {deploymentUsages} from "../lib/referenceUsage.js";
import {deploymentsS, machinesS, primaryConfigS, secretMetasS, secretsStatusS, spacesS, userConfigsS} from "../state/deployments.js";
import {containerWorkload} from "../lib/deploymentConfig.js";

const { div, h2, p, span, input, textarea, button, table, thead, tbody, tr, th, td, colgroup, col } = van.tags;
const DEFAULT_SECRET_MASK = "••••••••••••••••";
const RANDOM_SECRET_CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()-_=+[]{}";

const rawStateValue = (state) => state.rawVal ?? state.val ?? "";

const iconButton = (child, onclick, cls = "", attrs = {}) => button({
    type: "button",
    ...attrs,
    class: `inline-flex h-7 w-7 shrink-0 items-center justify-center rounded text-gray-400 hover:text-gray-100 ` +
        `hover:bg-surface-hover transition-colors cursor-pointer ${cls}`,
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
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);
    const usageTarget = van.state(null);
    const valueTarget = van.state(null);
    const createTarget = van.state(null);
    const createName = van.state("");
    const createValue = van.state("");
    const createError = van.state(null);
    const generatedSecretLength = van.state("32");
    let localRows = null;
    let streamSignature = '';
    let nextLocalKey = 1;
    const pendingDeletes = new Set();

    const errorBanner = () => error.val ? p({class: "text-red-400 text-sm"}, `Error: ${error.val}`) : "";

    const makeConfigRow = (config) => {
        const isNew = !config;
        return {
            localKey: `local:${nextLocalKey++}`,
            type: "config", isNew, _saved: false,
            referenceId: config ? config.id : 0,
            version: config ? config.version : 0,
            name: van.state(config ? config.name : ""),
            value: van.state(config ? config.value : ""),
            createdAt: config ? config.createdAt : null,
            copied: van.state(false),
            saving: van.state(false),
            nameAliases: new Set(),
            orig: {
                name: config ? config.name : "",
                value: config ? config.value : "",
            },
        };
    };

    const makeSecretRow = (meta) => {
        const isNew = !meta;
        return {
            localKey: `local:${nextLocalKey++}`,
            type: "secret", meta, isNew, _saved: false,
            referenceId: meta ? meta.id : 0,
            version: meta ? meta.version : 0,
            name: van.state(meta ? meta.name : ""),
            value: van.state(""),
            createdAt: meta ? meta.createdAt : null,
            loaded: van.state(isNew),
            copied: van.state(false),
            saving: van.state(false),
            nameAliases: new Set(),
            orig: {name: meta ? meta.name : "", value: ""},
        };
    };

    const nameDirty = (row) => row.name.val !== row.orig.name;
    const isDirty = (row) => row.isNew || nameDirty(row);

    const rowKey = (row) => row.orig.name ? `${row.type}:${row.orig.name}` : row.localKey;
    const itemKey = (type, item) => `${type}:${item.name}`;
    const latestByName = (items) => {
        const latest = new Map();
        for (const item of items || []) {
            const name = item?.name || "";
            if (!name) continue;
            const current = latest.get(name);
            if (!current || Number(item.version || 0) > Number(current.version || 0)) latest.set(name, item);
        }
        return Array.from(latest.values());
    };
    const settingConfigRefs = (settings) => [
        ["Web UI HTTP enabled", settings?.httpWeb?.enabled?.configRef?.id],
        ["Web UI HTTP listen", settings?.httpWeb?.listen?.configRef?.id],
        ["Web UI HTTPS enabled", settings?.httpsWeb?.enabled?.configRef?.id],
        ["Web UI HTTPS listen", settings?.httpsWeb?.listen?.configRef?.id],
        ["Web UI use self managed TLS cert", settings?.httpsWeb?.tlsSelfManaged?.configRef?.id],
        ["Web UI ACME hosts", settings?.httpsWeb?.acmeHosts?.configRef?.id],
        ["Web UI ACME email", settings?.httpsWeb?.acmeEmail?.configRef?.id],
        ["Cluster listen", settings?.cluster?.listen?.configRef?.id],
        ["Cluster enrollment listen", settings?.cluster?.enrollmentListen?.configRef?.id],
        ["Backup enabled", settings?.backup?.enabled?.configRef?.id],
        ["Backup S3 access key ID", settings?.backup?.s3AccessKeyId?.configRef?.id],
        ["Backup S3 bucket", settings?.backup?.s3Bucket?.configRef?.id],
        ["Backup S3 path", settings?.backup?.s3Path?.configRef?.id],
        ["Backup S3 region", settings?.backup?.s3Region?.configRef?.id],
        ["Backup S3 endpoint", settings?.backup?.s3Endpoint?.configRef?.id],
        ["Use separate large assets S3", settings?.largeAssets?.useSeparateS3?.configRef?.id],
        ["Large asset S3 access key ID", settings?.largeAssets?.s3AccessKeyId?.configRef?.id],
        ["Large asset S3 bucket", settings?.largeAssets?.s3Bucket?.configRef?.id],
        ["Large asset S3 path", settings?.largeAssets?.s3Path?.configRef?.id],
        ["Large asset S3 region", settings?.largeAssets?.s3Region?.configRef?.id],
        ["Large asset S3 endpoint", settings?.largeAssets?.s3Endpoint?.configRef?.id],
    ].map(([label, id]) => ({label, id: Number(id || 0)})).filter(ref => ref.id);
    const settingSecretRefs = (settings) => [
        ["Web UI TLS cert PEM", settings?.httpsWeb?.tlsCertPem?.id],
        ["GitHub token", settings?.repo?.githubToken?.id],
        ["Backup S3 secret access key", settings?.backup?.s3SecretAccessKey?.id],
        ["Large asset S3 secret access key", settings?.largeAssets?.s3SecretAccessKey?.id],
    ].map(([label, id]) => ({label, id: Number(id || 0)})).filter(ref => ref.id);
    const itemReferenceIDs = (row) => {
        const name = row.orig.name || rawStateValue(row.name).trim();
        if (!name) return new Set();
        const names = new Set([name, ...(row.nameAliases || [])]);
        return new Set((row.type === "secret" ? (secretMetasS.val || []) : (userConfigsS.val || []))
            .filter(item => names.has(item?.name))
            .map(item => Number(item.id || 0))
            .filter(Boolean));
    };
    const deploymentUsesItem = (deployment, row, referenceIDs = itemReferenceIDs(row)) => {
        if (!referenceIDs.size) return false;
        const cfg = deployment?.config;
        if (!cfg || cfg.deleted) return false;
        const envVars = containerWorkload(cfg)?.runtime?.envVars || {};
        return Object.values(envVars).some(value => referenceIDs.has(Number(value?.[row.type === "secret" ? "secretId" : "configId"] || 0)));
    };
    const usageForRow = (row) => {
        const referenceIDs = itemReferenceIDs(row);
        const settings = (row.type === "secret" ? settingSecretRefs(primaryConfigS.val?.config?.settings) : settingConfigRefs(primaryConfigS.val?.config?.settings))
            .filter(ref => referenceIDs.has(ref.id));
        const deployments = deploymentUsages(
            deploymentsS.val,
            spacesS.val,
            machinesS.val,
            deployment => deploymentUsesItem(deployment, row, referenceIDs),
        );
        return {deployments, settings};
    };
    const inUseCount = (row) => {
        const usage = usageForRow(row);
        return usage.deployments.length + usage.settings.length;
    };

    const sortValue = (row, key) => {
        if (key === "type") return row.type;
        if (key === "value") return row.type === "config" ? rawStateValue(row.value) : "";
        if (key === "created") return String(row.createdAt?.getTime() || 0).padStart(13, "0");
        if (key === "version") return String(row.version || 0).padStart(10, "0");
        if (key === "inUse") return String(inUseCount(row)).padStart(10, "0");
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

    const matchesSearch = (row, query) => !query ||
        row.type.includes(query) ||
        rawStateValue(row.name).toLowerCase().includes(query) ||
        (row.type === "config" && rawStateValue(row.value).toLowerCase().includes(query));

    const filteredAndSortedRows = (items) => {
        const query = search.val.trim().toLowerCase();
        return sortRows(query ? items.filter(row => matchesSearch(row, query)) : items);
    };

    const reconcileVisibleRows = (visible, nextAll) => {
        const query = search.val.trim().toLowerCase();
        const nextByKey = new Map(nextAll.map(row => [rowKey(row), row]));
        const displayed = new Set();
        const nextVisible = [];
        for (const row of visible || []) {
            const key = rowKey(row);
            if (displayed.has(key)) continue;
            const next = nextByKey.get(key);
            if (!next) continue;
            displayed.add(key);
            nextVisible.push(next);
        }
        for (const row of nextAll) {
            const key = rowKey(row);
            if (displayed.has(key)) continue;
            if (row.isNew || matchesSearch(row, query)) {
                displayed.add(key);
                nextVisible.push(row);
            }
        }
        return nextVisible;
    };

    const setLocalRows = (next, refreshVisible = false) => {
        localRows = next;
        rows.val = refreshVisible || rows.val === null
            ? filteredAndSortedRows(next)
            : reconcileVisibleRows(rows.val, next);
    };

    const syncRowsFromUniverse = (refreshVisible = false) => {
        const status = secretsStatusS.val;
        if (!status) return;
        const currentRows = localRows || [];
        const latestSecrets = latestByName(secretMetasS.val || []);
        const latestConfigs = latestByName(userConfigsS.val || []);
        const existing = new Map(currentRows
            .filter(row => !row.isNew && row.orig.name)
            .map(row => [rowKey(row), row]));
        const streamKeys = new Set([
            ...(status.unlocked ? latestSecrets.map(meta => itemKey("secret", meta)) : []),
            ...latestConfigs.map(config => itemKey("config", config)),
        ]);
        for (const row of currentRows) {
            for (const alias of row.nameAliases || []) {
                if (!streamKeys.has(`${row.type}:${alias}`)) row.nameAliases.delete(alias);
            }
        }
        for (const key of pendingDeletes) {
            if (!streamKeys.has(key)) pendingDeletes.delete(key);
        }
        const preserveOrMake = (key, make, confirmsSaved = () => false) => {
            const current = existing.get(key);
            if (!current) return make();
            if (current._saved && confirmsSaved(current)) current._saved = false;
            if (isDirty(current) || current._saved || current.saving.val) return current;
            const next = make();
            next.nameAliases = new Set(current.nameAliases || []);
            return next;
        };
        const secretRows = status.unlocked
            ? latestSecrets
                .filter(meta => !pendingDeletes.has(itemKey("secret", meta)))
                .map(meta => preserveOrMake(itemKey("secret", meta), () => makeSecretRow(meta), row => row.name.val.trim() === meta.name))
            : [];
        const configRows = latestConfigs
            .filter(config => !pendingDeletes.has(itemKey("config", config)))
            .map(config => preserveOrMake(itemKey("config", config), () => makeConfigRow(config), row => row.name.val.trim() === config.name && row.value.val === config.value));
        const carried = currentRows.filter(row => {
            if (row.saving.val) return pendingDeletes.has(rowKey(row)) || !streamKeys.has(rowKey(row));
            if (row.isNew && !row._saved) return true;
            return row._saved && row.orig.name && !streamKeys.has(rowKey(row));
        });
        setLocalRows([...secretRows, ...configRows, ...carried], refreshVisible);
    };

    van.derive(() => {
        const status = secretsStatusS.val;
        const signature = JSON.stringify({
            status,
            secrets: (secretMetasS.val || []).map(item => [item.id, item.name, item.version, item.createdAt, item.updatedBy]),
            configs: (userConfigsS.val || []).map(item => [item.id, item.name, item.version, item.value, item.createdAt, item.updatedBy]),
            deploymentRefs: (deploymentsS.val || []).map(item => [item.config?.id, item.config?.version, item.config?.deleted, containerWorkload(item.config)?.runtime?.envVars]),
            configVersion: primaryConfigS.val?.version,
        });
        if (signature === streamSignature) return;
        streamSignature = signature;
        syncRowsFromUniverse(sort.val.key === "inUse");
    });

    const openCreate = (type) => {
        createTarget.val = type;
        createName.val = "";
        createValue.val = "";
        createError.val = null;
        generatedSecretLength.val = "32";
    };
    const closeCreate = () => {
        createTarget.val = null;
        createError.val = null;
    };
    const generateSecret = () => {
        const length = Math.max(1, Math.min(4096, Number.parseInt(generatedSecretLength.val, 10) || 32));
        generatedSecretLength.val = String(length);
        const bytes = new Uint8Array(length);
        const limit = 256 - (256 % RANDOM_SECRET_CHARS.length);
        let result = "";
        while (result.length < length) {
            globalThis.crypto.getRandomValues(bytes);
            for (const byte of bytes) {
                if (byte < limit) result += RANDOM_SECRET_CHARS[byte % RANDOM_SECRET_CHARS.length];
                if (result.length === length) break;
            }
        }
        createValue.val = result;
    };
    const createResource = async () => {
        const type = createTarget.val;
        const name = createName.val.trim();
        if (!name) {
            createError.val = `${type === "secret" ? "Secret" : "Config"} name is required`;
            return;
        }
        try {
            createError.val = null;
            error.val = null;
            if (type === "secret") {
                await capi.postV1SecretsSet({name, value: new TextEncoder().encode(createValue.val)});
            } else {
                await capi.postV1UserConfigsSet({name, value: createValue.val});
            }
            closeCreate();
        } catch (e) {
            createError.val = e.message;
            error.val = e.message;
        }
    };
    const setSort = (key) => {
        const current = sort.val;
        sort.val = current.key === key
            ? {key, dir: current.dir === "asc" ? "desc" : "asc"}
            : {key, dir: "asc"};
        rows.val = filteredAndSortedRows(localRows || []);
    };

    const loadSecretValue = async (row) => {
        if (row.loaded.val || row.isNew) return true;
        try {
            error.val = null;
            const res = await capi.postV1SecretsReveal({id: row.referenceId});
            row.value.val = new TextDecoder().decode(res.value);
            row.orig.value = row.value.val;
            row.loaded.val = true;
            return true;
        } catch (e) {
            error.val = e.message;
            return false;
        }
    };

    const editRowValue = async (row) => {
        if (row.saving.val) return;
        if (row.type === "secret") {
            if (!await loadSecretValue(row)) return;
        }
        valueTarget.val = row;
    };

    const secretValueForCopy = async (row) => {
        if (row.isNew || row.loaded.val) return row.value.val;
        const res = await capi.postV1SecretsReveal({id: row.referenceId});
        const value = new TextDecoder().decode(res.value);
        row.value.val = value;
        row.orig.value = value;
        row.loaded.val = true;
        return value;
    };

    const copyRowValue = async (row) => {
        try {
            error.val = null;
            const value = row.type === "secret" ? await secretValueForCopy(row) : row.value.val;
            await navigator.clipboard.writeText(value);
            row.copied.val = true;
            setTimeout(() => { row.copied.val = false; }, 1500);
        } catch (e) {
            error.val = e.message;
        }
    };

    const saveName = async (row) => {
        if (row.isNew || row.saving.val) return;
        const name = row.name.val.trim();
        if (!name) { error.val = `${row.type === "secret" ? "Secret" : "Config"} name is required`; return; }
        if (name === row.orig.name) {
            row.name.val = row.orig.name;
            return;
        }
        const oldKey = rowKey(row);
        const oldName = row.orig.name;
        pendingDeletes.add(oldKey);
        row.saving.val = true;
        try {
            error.val = null;
            let saved;
            if (row.type === "secret") {
                saved = await capi.postV1SecretsRename({name: oldName, newName: name});
            } else {
                saved = await capi.postV1UserConfigsRename({name: oldName, newName: name});
            }
            row.nameAliases.add(oldName);
            row.referenceId = Number(saved?.id || row.referenceId || 0);
            row.version = Number(saved?.version || row.version || 0);
            row.createdAt = saved?.createdAt || row.createdAt;
            if (row.type === "secret" && saved) row.meta = saved;
            row.name.val = name;
            row._saved = true;
            row.orig.name = name;
            syncRowsFromUniverse();
        } catch (e) {
            pendingDeletes.delete(oldKey);
            error.val = e.message;
            syncRowsFromUniverse();
        } finally {
            row.saving.val = false;
        }
    };

    const saveValue = async (row, value) => {
        if (row.saving.val) return;
        const wasNew = row.isNew;
        const name = row.isNew ? row.name.val.trim() : row.orig.name;
        if (!name) { error.val = `${row.type === "secret" ? "Secret" : "Config"} name is required`; return; }
        row.saving.val = true;
        try {
            error.val = null;
            let saved;
            if (row.type === "config") {
                saved = await capi.postV1UserConfigsSet({name, value});
            } else {
                saved = await capi.postV1SecretsSet({name, value: new TextEncoder().encode(value)});
                row.loaded.val = true;
            }
            row.value.val = value;
            row.orig.value = value;
            row.referenceId = Number(saved?.id || row.referenceId || 0);
            row.version = Number(saved?.version || row.version || 0);
            row.createdAt = saved?.createdAt || row.createdAt;
            if (row.type === "secret" && saved) row.meta = saved;
            row.isNew = false;
            if (wasNew) {
                row.name.val = name;
                row.orig.name = name;
            }
            row._saved = true;
            syncRowsFromUniverse();
            return true;
        } catch (e) {
            error.val = e.message;
            throw e;
        } finally {
            row.saving.val = false;
        }
    };

    const discardName = (row) => {
        row.name.val = row.orig.name;
    };

    const deleteRow = async (row) => {
        try {
            error.val = null;
            if (row.type === "secret") await capi.postV1SecretsDelete({name: row.orig.name});
            else await capi.postV1UserConfigsDelete({name: row.orig.name});
            pendingDeletes.add(rowKey(row));
            return true;
        } catch (e) {
            error.val = e.message;
            return false;
        }
    };

    const requestDeleteRow = (row) => {
        deleteTarget.val = row;
    };

    const cancelDelete = () => {
        if (deleteSaving.val) return;
        deleteTarget.val = null;
    };

    const confirmDelete = async () => {
        const row = deleteTarget.val;
        if (!row || deleteSaving.val) return;
        deleteSaving.val = true;
        const deleted = await deleteRow(row);
        deleteSaving.val = false;
        if (deleted) deleteTarget.val = null;
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

    const typeBadge = (type) => span({
        class: `inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${type === "secret"
            ? "bg-purple-500/15 text-purple-300"
            : "bg-blue-500/15 text-blue-300"}`,
    }, type === "secret" ? "Secret" : "Config");

    const valueCell = (row) => div({
        class: "w-full truncate rounded px-2 py-1 font-mono text-gray-300",
        title: row.type === "config" ? () => row.value.val : "Secret value",
    }, () => row.type === "secret" ? DEFAULT_SECRET_MASK : row.value.val);

    const usageButton = (row) => {
        const usage = usageForRow(row);
        const count = usage.deployments.length + usage.settings.length;
        if (!count) return "0";
        const name = row.orig.name || rawStateValue(row.name).trim();
        return button({
            type: "button",
            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
            "aria-label": `Show usage for ${row.type} ${name}`,
            onclick: () => usageTarget.val = {resourceType: row.type, resourceName: name, ...usage},
        }, String(count));
    };

    const nameInput = (row) => inlineEditableInput({
        value: row.name,
        dirty: () => !row.isNew && nameDirty(row),
        valid: () => Boolean(row.name.val.trim()),
        disabled: row.saving,
        oninput: event => { row.name.val = event.target.value; },
        onSave: () => saveName(row),
        onDiscard: () => discardName(row),
        inputClass: "w-full bg-transparent px-2 py-1 rounded border border-transparent hover:border-gray-700 focus:border-brand focus:outline-none font-mono",
        placeholder: "name",
        ariaLabel: `${row.type === "secret" ? "Secret" : "Config"} name ${row.orig.name}`,
        saveAriaLabel: `Save ${row.type} name ${row.orig.name}`,
        discardAriaLabel: `Discard ${row.type} name change for ${row.orig.name}`,
    });

    const rowEl = (row) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-1 pr-3 w-px whitespace-nowrap"}, typeBadge(row.type)),
        td({class: "py-1 pr-3 w-1/3"}, nameInput(row)),
        td({class: "py-1 pr-3 text-gray-300 whitespace-nowrap"}, `v${row.version || 0}`),
        td({class: "py-1 pr-3 text-gray-400 whitespace-nowrap"}, formatDateTime(row.createdAt, "-")),
        td({class: "py-1 pr-3 text-gray-400 whitespace-nowrap tabular-nums"}, () => usageButton(row)),
        td({class: "py-1 pr-3 min-w-0"}, valueCell(row)),
        td({class: "py-1 pl-2 pr-1 text-right whitespace-nowrap w-px"},
            div({class: "flex items-center justify-end gap-1"},
                iconButton(editIcon(), () => { void editRowValue(row); },
                    "disabled:cursor-not-allowed disabled:opacity-50", {
                        title: `Edit ${row.type} value`,
                        "aria-label": `Edit ${row.type} value`,
                        disabled: row.saving,
                    }),
                row.type === "secret"
                    ? iconButton(eyeOpenIcon(), () => { void editRowValue(row); }, "disabled:cursor-not-allowed disabled:opacity-50", {
                            title: "View or edit secret value",
                            "aria-label": "View or edit secret value",
                            disabled: row.saving,
                        })
                    : iconButton(expandIcon(), () => { void editRowValue(row); },
                        "disabled:cursor-not-allowed disabled:opacity-50", {
                            title: "View or edit config value",
                            "aria-label": "View or edit config value",
                            disabled: row.saving,
                        }),
                iconButton(() => row.copied.val
                    ? checkIcon({class: "w-4 h-4 text-green-400"})
                    : copyIcon(), () => { void copyRowValue(row); }, "disabled:cursor-not-allowed disabled:opacity-50", {
                    title: () => row.copied.val ? "Copied" : `Copy ${row.type} value`,
                    "aria-label": () => row.copied.val ? "Copied" : `Copy ${row.type} value`,
                    disabled: row.saving,
                }),
                iconButton(trashIcon(), () => requestDeleteRow(row),
                    "hover:text-red-400 disabled:cursor-not-allowed disabled:opacity-50", {
                        title: `Delete ${row.type}`,
                        "aria-label": `Delete ${row.type}`,
                        disabled: row.saving,
                    }))),
    );

    const deleteOverlay = () => {
        const row = deleteTarget.val;
        if (!row) return "";
        const typeLabel = row.type === "secret" ? "secret" : "config";
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div(
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "text-sm text-gray-300"}, `Are you sure you want to delete ${typeLabel} ${row.orig.name}?`),
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", cancelDelete, "bg-gray-700 text-gray-200 hover:bg-gray-600", () => deleteSaving.val),
                    spinnerButton("Confirm", confirmDelete,
                        "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500",
                        "button", () => deleteSaving.val),
                ),
            ),
        );
    };

    const usageOverlay = () => {
        const target = usageTarget.val;
        if (!target) return "";
        return referenceUsageOverlay(
            target.resourceType,
            target.resourceName,
            target.deployments,
            target.settings,
            () => usageTarget.val = null,
        );
    };

    const valueViewerOverlay = () => {
        const row = valueTarget.val;
        if (!row) return "";
        const usage = usageForRow(row);
        return valueOverlay({
            name: rawStateValue(row.name),
            type: row.type,
            value: () => row.value.val,
            version: row.version || 0,
            createdAt: row.createdAt,
            referenceCount: usage.deployments.length + usage.settings.length,
            deploymentCount: usage.deployments.length,
            onSave: (value) => saveValue(row, value),
            onClose: () => valueTarget.val = null,
        });
    };

    const createOverlay = () => {
        const type = createTarget.val;
        if (!type) return "";
        const typeLabel = type === "secret" ? "secret" : "config";
        return div(
            div({class: "fixed inset-0 z-40 bg-black/70", onclick: closeCreate}),
            div(
                {class: "fixed inset-0 z-50 flex items-center justify-center p-4 pointer-events-none", "data-testid": `create-${typeLabel}-overlay`},
                div(
                    {
                        class: "card w-full max-w-lg flex flex-col gap-4 shadow-2xl pointer-events-auto",
                        role: "dialog",
                        "aria-modal": "true",
                        "aria-labelledby": "create-resource-title",
                        onclick: (e) => e.stopPropagation(),
                    },
                    h2({id: "create-resource-title", class: "text-base font-semibold"}, `Add ${typeLabel}`),
                    div({class: "flex flex-col gap-1.5"},
                        p({class: "text-xs font-medium text-gray-400"}, "Name"),
                        input({
                            class: "text-input font-mono",
                            placeholder: `${typeLabel} name`,
                            autocomplete: "off",
                            value: createName,
                            oninput: (e) => createName.val = e.target.value,
                        }),
                    ),
                    type === "secret" ? div(
                        {class: "flex flex-wrap items-end justify-between gap-3 rounded-lg border border-gray-700 bg-gray-950/50 p-3"},
                        div({class: "flex flex-col gap-1.5"},
                            p({class: "text-xs font-medium text-gray-400"}, "Generate random value"),
                            input({
                                class: "text-input w-24 py-1.5 text-sm font-mono",
                                type: "number",
                                min: "1",
                                max: "4096",
                                value: generatedSecretLength,
                                oninput: (e) => generatedSecretLength.val = e.target.value,
                                "aria-label": "Generated secret length",
                            }),
                        ),
                        smallBtn("Generate", generateSecret, "bg-gray-700 text-gray-200 hover:bg-gray-600"),
                    ) : "",
                    div({class: "flex min-h-0 flex-col gap-1.5"},
                        p({class: "text-xs font-medium text-gray-400"}, "Value"),
                        textarea({
                            class: "text-input min-h-44 resize-y font-mono text-sm leading-relaxed",
                            placeholder: `${typeLabel} value`,
                            autocomplete: "off",
                            spellcheck: "false",
                            value: createValue,
                            oninput: (e) => createValue.val = e.target.value,
                        }),
                    ),
                    () => createError.val ? p({class: "text-sm text-red-400"}, createError.val) : "",
                    div({class: "flex items-center justify-end gap-2"},
                        smallBtn("Cancel", closeCreate, "bg-gray-700 text-gray-200 hover:bg-gray-600"),
                        spinnerButton(`Add ${typeLabel}`, createResource, "btn-primary text-sm py-1.5 px-3", "button",
                            () => !createName.val.trim()),
                    ),
                ),
            ),
        );
    };

    const tableClass = "w-full min-w-[84rem] table-fixed text-sm";

    const tableCols = () => colgroup(
        col({style: "width:7rem"}),
        col({style: "width:18rem"}),
        col({style: "width:7rem"}),
        col({style: "width:12rem"}),
        col({style: "width:8rem"}),
        col({style: "width:22rem"}),
        col({style: "width:9rem"}),
    );

    const sortableHeader = (key, label, cls = "") => th({class: `pb-2 pr-3 font-medium ${cls}`},
        button({
            type: "button",
            class: "inline-flex items-center gap-1 text-gray-400 hover:text-gray-100 cursor-pointer",
            onclick: () => setSort(key),
        }, label, () => sort.val.key === key ? (sort.val.dir === "asc" ? " ^" : " v") : ""));

    const tableHeader = () => table(
        {class: tableClass},
        tableCols(),
        thead(
            tr({class: "text-left text-gray-400 border-b border-gray-700"},
                sortableHeader("type", "Type", "w-px"),
                sortableHeader("name", "Name"),
                sortableHeader("version", "Version"),
                sortableHeader("created", "Created"),
                sortableHeader("inUse", "In use by"),
                sortableHeader("value", "Value"),
                th({class: "pb-2 pr-1 w-px"}, ""),
            )),
    );

    const tableBody = (visibleRows) => table(
        {class: tableClass},
        tableCols(),
        tbody(...visibleRows.map(rowEl)),
    );

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
                oninput: (e) => {
                    search.val = e.target.value;
                    rows.val = filteredAndSortedRows(localRows || []);
                },
            }),
            div({class: "flex flex-wrap items-center gap-2"},
                actionButton("Add secret", () => openCreate("secret"), "bg-gray-700 text-gray-200 hover:bg-gray-600",
                    () => !secretsStatusS.val || !secretsStatusS.val.unlocked),
                actionButton("Add config", () => openCreate("config")))),
        div({class: "flex-1 min-h-0 overflow-hidden"}, () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            if (rows.val.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No secrets or configs yet.");
            }
            const visibleRows = rows.val;
            if (visibleRows.length === 0) {
                return p({class: "text-gray-400 text-sm"}, "No secrets or configs match your search.");
            }
            return div(
                {class: "app-scroll-x h-full min-h-0 overflow-x-auto overflow-y-hidden"},
                div(
                    {class: "h-full min-h-0 flex flex-col"},
                    div({class: "flex-none pr-1"}, tableHeader()),
                    div({class: "deployment-table-scroll min-h-0 flex-1 overflow-y-auto overflow-x-hidden pr-1"}, tableBody(visibleRows)),
                ),
            );
        }),
    );

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3"},
        contentTable,
        deleteOverlay,
        usageOverlay,
        valueViewerOverlay,
        createOverlay,
    );
}
