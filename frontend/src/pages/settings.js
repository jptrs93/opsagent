import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {codeBlock} from "../components/codeBlock.js";
import {referencePicker} from "../components/referencePicker.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {valueOverlay} from "../components/valueOverlay.js";
import {
    checkIcon, closeIcon, copyIcon, eyeOffIcon, eyeOpenIcon, infoIcon, plusIcon, searchIcon, secretKeyIcon,
} from "../lib/icons.js";
import {secretRefsS, secretsStatusS, systemConfigS, userConfigRefsS} from "../state/deployments.js";

const {button, col, colgroup, div, h2, input, label: labelEl, option, p, pre, select, span, table, tbody, td, tr} = van.tags;

const boolValue = (value) => value ? "true" : "false";
const shellQuote = (value) => {
    const s = String(value ?? "");
    if (!s) return "''";
    if (/^\$[A-Za-z_][A-Za-z0-9_]*$/.test(s)) return s;
    if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s;
    return `'${s.replace(/'/g, `'"'"'`)}'`;
};

const refID = (ref) => Number(ref?.versionId || 0);
const deepClone = (value) => JSON.parse(JSON.stringify(value));
const stringSetting = (value = "") => ({value, configRef: undefined});
const boolSetting = (value = false) => ({value, configRef: undefined});
const secretSetting = (id = 0) => (id ? {versionId: id} : {});
const latestRefs = (refs, selectedID = 0) => {
    const latest = new Map();
    const byID = new Map();
    for (const ref of refs || []) {
        const name = ref?.name || "";
        if (!name || !ref?.id) continue;
        byID.set(Number(ref.id), ref);
        const current = latest.get(name);
        if (!current || Number(ref.version || 0) > Number(current.version || 0)) latest.set(name, ref);
    }
    const options = Array.from(latest.values());
    const selected = byID.get(Number(selectedID || 0));
    if (selected && !options.some(ref => Number(ref.id) === Number(selected.id))) options.push(selected);
    return options.sort((a, b) => (a.name || "").localeCompare(b.name || "") || Number(a.version || 0) - Number(b.version || 0));
};
const refLabel = (ref) => `${ref.name} v${ref.version || 0}`;
const findRef = (refs, id) => (refs || []).find(ref => Number(ref.id || 0) === Number(id || 0));
const configRefPayload = (item) => ({versionId: Number(item.configRefID || 0)});
const secretRefPayload = (item) => ({versionId: Number(item.secretId || 0)});
const emptySettings = () => ({
    httpWeb: {
        enabled: boolSetting(false),
        listen: stringSetting(":8080"),
    },
    httpsWeb: {
        enabled: boolSetting(true),
        listen: stringSetting(":443"),
        tlsSelfManaged: boolSetting(false),
        tlsCertPem: secretSetting(),
        acmeHosts: stringSetting("opendeploy.dev"),
        acmeEmail: stringSetting(""),
    },
    cluster: {
        listen: stringSetting(":9443"),
        enrollmentListen: stringSetting(":9444"),
    },
    auth: {
        passwordLoginEnabled: boolSetting(false),
    },
    repo: {
        githubToken: secretSetting(),
    },
    backup: {
        enabled: boolSetting(false),
        s3AccessKeyId: stringSetting(""),
        s3SecretAccessKey: secretSetting(),
        s3Bucket: stringSetting(""),
        s3Path: stringSetting("opendeploy/primary"),
        s3Region: stringSetting("us-east-1"),
        s3Endpoint: stringSetting(""),
    },
    largeAssets: {
        useSeparateS3: boolSetting(false),
        s3AccessKeyId: stringSetting(""),
        s3SecretAccessKey: secretSetting(),
        s3Bucket: stringSetting(""),
        s3Path: stringSetting("opendeploy/assets"),
        s3Region: stringSetting("us-east-1"),
        s3Endpoint: stringSetting(""),
        keepLocalCopy: boolSetting(false),
    },
});

const resolvedConfigValue = (id) => {
    id = Number(id || 0);
    if (!id) return "";
    return findRef(userConfigRefsS.val, id)?.value || "";
};

const effectiveStringSettingValue = (setting, fallback = "") => {
    if (!setting) return fallback;
    const id = refID(setting.configRef);
    if (id) return resolvedConfigValue(id) || fallback;
    return setting.value ?? fallback;
};

const parsedBoolValue = (value) => {
    const normalized = String(value ?? "").trim().toLowerCase();
    if (["1", "t", "true"].includes(normalized)) return true;
    if (["0", "f", "false"].includes(normalized)) return false;
    return undefined;
};

const effectiveBoolSettingValue = (setting, fallback = false) => {
    if (!setting) return fallback;
    const id = refID(setting.configRef);
    if (id) {
        return parsedBoolValue(resolvedConfigValue(id)) ?? fallback;
    }
    return Boolean(setting.value);
};

const effectiveDraftBoolValue = (item, fallback = false) => {
    if (!item) return fallback;
    if (item.mode === "config") {
        return parsedBoolValue(resolvedConfigValue(item.configRefID)) ?? fallback;
    }
    return parsedBoolValue(item.value) ?? fallback;
};

const settingsSections = [
    {
        key: "web",
        title: "Web UI",
        settings: [
            {label: "Web UI HTTPS enabled", key: "WEB_HTTPS_ENABLED", type: "bool", setting: (cfg) => cfg.httpsWeb?.enabled, apply: (doc, item) => { doc.httpsWeb.enabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI HTTPS listen", key: "WEB_HTTPS_LISTEN", type: "text", setting: (cfg) => cfg.httpsWeb?.listen, apply: (doc, item) => { doc.httpsWeb.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI use self managed TLS cert", key: "WEB_TLS_SELF_MANAGED", type: "bool", setting: (cfg) => cfg.httpsWeb?.tlsSelfManaged, apply: (doc, item) => { doc.httpsWeb.tlsSelfManaged = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI TLS cert PEM", key: "WEB_TLS_CERT_PEM", type: "secret", secret: (cfg) => cfg.httpsWeb?.tlsCertPem, apply: (doc, item) => { doc.httpsWeb.tlsCertPem = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.web_tls_cert_pem", visible: (draft) => effectiveDraftBoolValue(draft?.WEB_TLS_SELF_MANAGED)},
            {label: "Web UI hostnames (also ACME hosts)", key: "ACME_HOSTS", type: "text", setting: (cfg) => cfg.httpsWeb?.acmeHosts, apply: (doc, item) => { doc.httpsWeb.acmeHosts = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI ACME email", key: "ACME_EMAIL", type: "text", setting: (cfg) => cfg.httpsWeb?.acmeEmail, apply: (doc, item) => { doc.httpsWeb.acmeEmail = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI HTTP enabled", key: "WEB_HTTP_ENABLED", type: "bool", setting: (cfg) => cfg.httpWeb?.enabled, apply: (doc, item) => { doc.httpWeb.enabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI HTTP listen", key: "WEB_HTTP_LISTEN", type: "text", setting: (cfg) => cfg.httpWeb?.listen, apply: (doc, item) => { doc.httpWeb.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }, visible: (draft) => effectiveDraftBoolValue(draft?.WEB_HTTP_ENABLED)},
        ],
    },
    {
        key: "auth",
        title: "Authentication",
        settings: [
            {label: "Master password login enabled", key: "PASSWORD_LOGIN_ENABLED", type: "bool", setting: (cfg) => cfg.auth?.passwordLoginEnabled, apply: (doc, item) => { (doc.auth ||= {}).passwordLoginEnabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
        ],
    },
    {
        key: "cluster",
        title: "Cluster",
        settings: [
            {label: "Cluster listen", key: "CLUSTER_LISTEN", type: "text", setting: (cfg) => cfg.cluster?.listen, apply: (doc, item) => { doc.cluster.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Cluster enrollment listen", key: "ENROLLMENT_LISTEN", type: "text", setting: (cfg) => cfg.cluster?.enrollmentListen, apply: (doc, item) => { doc.cluster.enrollmentListen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
        ],
    },
    {
        key: "repo",
        title: "Repository credentials",
        settings: [
            {label: "GitHub token", key: "GITHUB_TOKEN", type: "secret", secret: (cfg) => cfg.repo?.githubToken, apply: (doc, item) => { (doc.repo ||= {}).githubToken = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.github_token"},
        ],
    },
    {
        key: "backup",
        title: "Backup",
        enabledKey: "BACKUP_ENABLED",
        settings: [
            {label: "Backup enabled", key: "BACKUP_ENABLED", type: "bool", setting: (cfg) => cfg.backup?.enabled, apply: (doc, item) => { doc.backup.enabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Backup S3 access key ID", key: "BACKUP_S3_ACCESS_KEY_ID", type: "text", setting: (cfg) => cfg.backup?.s3AccessKeyId, apply: (doc, item) => { doc.backup.s3AccessKeyId = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Backup S3 secret access key", key: "BACKUP_S3_SECRET_ACCESS_KEY", type: "secret", secret: (cfg) => cfg.backup?.s3SecretAccessKey, apply: (doc, item) => { doc.backup.s3SecretAccessKey = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.backup_s3_secret_access_key"},
            {label: "Backup S3 bucket", key: "BACKUP_S3_BUCKET", type: "text", setting: (cfg) => cfg.backup?.s3Bucket, apply: (doc, item) => { doc.backup.s3Bucket = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Backup S3 path", key: "BACKUP_S3_PATH", type: "text", setting: (cfg) => cfg.backup?.s3Path, apply: (doc, item) => { doc.backup.s3Path = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Backup S3 region", key: "BACKUP_S3_REGION", type: "text", setting: (cfg) => cfg.backup?.s3Region, apply: (doc, item) => { doc.backup.s3Region = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Backup S3 endpoint", key: "BACKUP_S3_ENDPOINT", type: "text", setting: (cfg) => cfg.backup?.s3Endpoint, apply: (doc, item) => { doc.backup.s3Endpoint = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Keep local copies of large assets", key: "LARGE_ASSETS_KEEP_LOCAL_COPY", type: "bool", setting: (cfg) => cfg.largeAssets?.keepLocalCopy, apply: (doc, item) => { doc.largeAssets.keepLocalCopy = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }, visible: (draft) => effectiveDraftBoolValue(draft?.BACKUP_ENABLED)},
            {label: "Large asset S3 path", key: "LARGE_ASSET_S3_PATH", type: "text", setting: (cfg) => cfg.largeAssets?.s3Path, apply: (doc, item) => { doc.largeAssets.s3Path = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Use separate large assets S3", key: "LARGE_ASSETS_USE_SEPARATE_S3", type: "bool", setting: (cfg) => cfg.largeAssets?.useSeparateS3, apply: (doc, item) => { doc.largeAssets.useSeparateS3 = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Large asset S3 access key ID", key: "LARGE_ASSET_S3_ACCESS_KEY_ID", type: "text", setting: (cfg) => cfg.largeAssets?.s3AccessKeyId, apply: (doc, item) => { doc.largeAssets.s3AccessKeyId = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }, visible: (draft) => effectiveDraftBoolValue(draft?.LARGE_ASSETS_USE_SEPARATE_S3)},
            {label: "Large asset S3 secret access key", key: "LARGE_ASSET_S3_SECRET_ACCESS_KEY", type: "secret", secret: (cfg) => cfg.largeAssets?.s3SecretAccessKey, apply: (doc, item) => { doc.largeAssets.s3SecretAccessKey = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.large_asset_s3_secret_access_key", visible: (draft) => effectiveDraftBoolValue(draft?.LARGE_ASSETS_USE_SEPARATE_S3)},
            {label: "Large asset S3 bucket", key: "LARGE_ASSET_S3_BUCKET", type: "text", setting: (cfg) => cfg.largeAssets?.s3Bucket, apply: (doc, item) => { doc.largeAssets.s3Bucket = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }, visible: (draft) => effectiveDraftBoolValue(draft?.LARGE_ASSETS_USE_SEPARATE_S3)},
            {label: "Large asset S3 region", key: "LARGE_ASSET_S3_REGION", type: "text", setting: (cfg) => cfg.largeAssets?.s3Region, apply: (doc, item) => { doc.largeAssets.s3Region = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }, visible: (draft) => effectiveDraftBoolValue(draft?.LARGE_ASSETS_USE_SEPARATE_S3)},
            {label: "Large asset S3 endpoint", key: "LARGE_ASSET_S3_ENDPOINT", type: "text", setting: (cfg) => cfg.largeAssets?.s3Endpoint, apply: (doc, item) => { doc.largeAssets.s3Endpoint = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }, visible: (draft) => effectiveDraftBoolValue(draft?.LARGE_ASSETS_USE_SEPARATE_S3)},
        ],
    },
];

const settings = settingsSections.flatMap((section) => section.settings);
const settingUsesConfigRef = (setting) => setting.type !== "secret";

const draftValue = (setting, cfg) => {
    if (setting.type === "secret") {
        const secret = setting.secret(cfg);
        return {
            value: "",
            secretId: refID(secret),
            originalSecretId: refID(secret),
        };
    }
    const current = setting.setting(cfg) || {};
    const refId = refID(current.configRef);
    const original = setting.type === "bool"
        ? boolValue(current.value)
        : (current.value || "");
    return {
        value: original,
        original,
        mode: refId ? "config" : "value",
        originalMode: refId ? "config" : "value",
        configRefID: refId,
        originalConfigRefID: refId,
    };
};

const configDraft = (cfg) => Object.fromEntries(settings.map((setting) => [setting.key, draftValue(setting, cfg)]));

const isDirty = (setting, item) => {
    if (setting.type === "secret") return Number(item.secretId || 0) !== Number(item.originalSecretId || 0);
    if (item.mode !== item.originalMode) return true;
    if (item.mode === "config") return Number(item.configRefID || 0) !== Number(item.originalConfigRefID || 0);
    return item.value !== item.original;
};

const dirtySettingsFor = (draft) => settings
    .map((setting) => ({setting, item: draft?.[setting.key]}))
    .filter(({setting, item}) => item && isDirty(setting, item));

const describeOriginal = (setting, item) => {
    if (setting.type === "secret") {
        const ref = findRef(secretRefsS.val, item.originalSecretId);
        return item.originalSecretId ? `secret ${ref ? refLabel(ref) : `#${item.originalSecretId}`}` : "no secret";
    }
    if (item.originalMode === "config") {
        const ref = findRef(userConfigRefsS.val, item.originalConfigRefID);
        return item.originalConfigRefID ? `config ${ref ? refLabel(ref) : `#${item.originalConfigRefID}`}` : "no config";
    }
    return item.original === "" ? "empty" : JSON.stringify(item.original);
};

const rowHint = (setting, item) => {
    if (!item) return "";
    if (isDirty(setting, item)) return `was ${describeOriginal(setting, item)}`;
    if (setting.type !== "secret" && item.mode === "config" && item.configRefID) {
        const resolved = resolvedConfigValue(item.configRefID);
        return `→ ${resolved === "" ? "empty" : JSON.stringify(resolved)}`;
    }
    return "";
};

const overlayClass = "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4";
const dialogClass = "card w-full max-w-md flex flex-col gap-3 shadow-2xl";
const cellClass = "px-2 py-[3px] align-middle";
const controlClass = "input h-6 w-full min-w-0 text-xs";
const smallButtonClass = "text-xs px-3 py-1 rounded-md font-medium cursor-pointer whitespace-nowrap";
const neutralButtonClass = `${smallButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600`;
const primaryButtonClass = "bg-brand text-white hover:bg-blue-600";
const rowButtonBase = "inline-flex h-6 items-center gap-1 whitespace-nowrap rounded border px-2 text-[11px]";
const rowButtonClass = `${rowButtonBase} border-gray-600 text-gray-300 hover:bg-surface-hover cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed`;
const iconButtonClass = "inline-flex h-6 w-6 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-gray-500";
const hintClass = "min-w-0 flex-1 truncate font-mono text-[11px] text-gray-500";
const mutedClass = "text-[11px] text-gray-500";

const iconButton = (icon, title, onclick, attrs = {}) => button({
    type: "button",
    title,
    "aria-label": title,
    class: iconButtonClass,
    onclick,
    ...attrs,
}, icon);

const focusLater = (el) => { setTimeout(() => el.focus(), 0); return el; };

// Built once by the dashboard, outside its page-switch binding, and kept
// mounted but hidden while other pages show (see docs/engineering/frontend.md).
// The rows read `draft` in their bindings, so building the page inside the
// switch would make every draft edit rebuild the whole page; and caching a
// detached node instead loses its derives to VanJS's collector, which left the
// page stuck on "Loading..." after a sign-out.
export function settingsPage() {
    const draft = van.state(null);
    const dirtyCount = van.state(0);
    const loaded = van.state(false);
    const settingsChangedElsewhere = van.state(false);
    const error = van.state(null);
    const saving = van.state(false);
    const search = van.state("");
    const recoveryCode = van.state("");
    const recoveryCodeCopied = van.state(false);
    const restoreCommandOpen = van.state(false);
    const masterPasswordVerifyValue = van.state("");
    const masterPasswordVerifyResult = van.state("");
    const masterPasswordVerifyOK = van.state(false);
    const masterPasswordVerifyOverlay = van.state(false);
    const masterPasswordOverlay = van.state(false);
    const newMasterPassword = van.state("");
    const newMasterPasswordRevealed = van.state(false);
    const newMasterPasswordCopied = van.state(false);
    const createSecretTarget = van.state(null);
    const editSecretTarget = van.state(null);
    const openingSecretID = van.state(0);

    const setDraft = (next) => {
        draft.val = next;
        dirtyCount.val = dirtySettingsFor(next).length;
    };

    const patchDraft = (key, next) => {
        const current = draft.val;
        if (!current?.[key]) return;
        setDraft({...current, [key]: {...current[key], ...next}});
    };

    const currentSettings = () => systemConfigS.val?.config?.settings || null;
    let loadedConfigVersion = 0;
    van.derive(() => {
        const versioned = systemConfigS.val;
        const settings = versioned?.config?.settings;
        if (!settings) {
            loadedConfigVersion = 0;
            if (loaded.val) {
                draft.val = null;
                dirtyCount.val = 0;
                loaded.val = false;
                settingsChangedElsewhere.val = false;
            }
            return;
        }
        if (Number(versioned.version) === loadedConfigVersion) return;
        loadedConfigVersion = Number(versioned.version);
        loaded.val = true;
        if (!draft.val || dirtySettingsFor(draft.val).length === 0) {
            setDraft(configDraft(settings));
            settingsChangedElsewhere.val = false;
        } else {
            settingsChangedElsewhere.val = true;
        }
    });

    const dirtySettings = () => dirtySettingsFor(draft.val);

    const resetChanges = () => {
        const settings = currentSettings();
        if (!settings) return;
        setDraft(configDraft(settings));
        settingsChangedElsewhere.val = false;
    };

    const openCreateSecret = (setting) => {
        createSecretTarget.val = {
            settingKey: setting.key,
            name: setting.defaultSecretName || "",
        };
    };

    const closeCreateSecret = () => {
        createSecretTarget.val = null;
    };

    const saveCreatedSecret = async (value, name) => {
        const target = createSecretTarget.val;
        if (!target) throw new Error("No setting selected for the new secret");
        try {
            error.val = null;
            const saved = await capi.postV1SecretsCreate({
                name,
                value: new TextEncoder().encode(value),
            });
            patchDraft(target.settingKey, {secretId: Number(saved?.eventId || 0)});
        } catch (e) {
            error.val = e.message;
            throw e;
        }
    };

    const openEditSecret = async (setting) => {
        const id = Number(draft.val?.[setting.key]?.secretId || 0);
        if (!id || openingSecretID.val) return;
        openingSecretID.val = id;
        try {
            error.val = null;
            const res = await capi.postV1SecretsReveal({id});
            const meta = findRef(secretRefsS.val, id);
            if (!meta) throw new Error("Selected secret metadata is unavailable");
            editSecretTarget.val = {
                settingKey: setting.key,
                id,
                stableId: Number(meta.stableId || 0),
                name: meta.name,
                version: Number(meta.version || 0),
                createdAt: meta.createdAt,
                value: new TextDecoder().decode(res.value),
            };
        } catch (e) {
            error.val = e.message;
        } finally {
            openingSecretID.val = 0;
        }
    };

    const closeEditSecret = () => {
        editSecretTarget.val = null;
    };

    const saveEditedSecret = async (value) => {
        const target = editSecretTarget.val;
        if (!target) throw new Error("No secret selected for editing");
        try {
            error.val = null;
            const saved = await capi.postV1SecretsSet({
                secretId: target.stableId,
                value: new TextEncoder().encode(value),
            });
            patchDraft(target.settingKey, {secretId: Number(saved?.eventId || 0)});
        } catch (e) {
            error.val = e.message;
            throw e;
        }
    };

    const saveChanges = async () => {
        if (saving.val) return;
        try {
            saving.val = true;
            error.val = null;
            const payload = deepClone(currentSettings() || emptySettings());
            dirtySettings().forEach(({setting, item}) => setting.apply(payload, item));
            const res = await capi.postV1ClusterSettingsUpdate(payload);
            setDraft(configDraft(res));
            settingsChangedElsewhere.val = false;
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    const generateRecovery = async () => {
        try {
            error.val = null;
            const res = await capi.postV1SecretsRotateRecoveryCode();
            recoveryCodeCopied.val = false;
            recoveryCode.val = res.code;
        } catch (e) {
            error.val = e.message;
        }
    };

    const copyRecoveryCode = async () => {
        if (!recoveryCode.val) return;
        await navigator.clipboard.writeText(recoveryCode.val);
        recoveryCodeCopied.val = true;
        setTimeout(() => { recoveryCodeCopied.val = false; }, 1500);
    };

    const recoveryInstallExample = () => {
        const cfg = currentSettings() || {};
        const httpWeb = cfg.httpWeb || {};
        const httpsWeb = cfg.httpsWeb || {};
        const backup = cfg.backup || {};
        const httpOnly = effectiveBoolSettingValue(httpWeb.enabled, false) && !effectiveBoolSettingValue(httpsWeb.enabled, true);
        const webListen = httpOnly
            ? effectiveStringSettingValue(httpWeb.listen, ":8080")
            : effectiveStringSettingValue(httpsWeb.listen, ":443");
        const args = [
            ["--http-only", boolValue(httpOnly)],
            ["--password-login", boolValue(effectiveBoolSettingValue(cfg.auth?.passwordLoginEnabled, false))],
            ["--web-listen", webListen || ":443"],
            ["--cluster-listen", effectiveStringSettingValue(cfg.cluster?.listen, ":9443")],
            ["--enrollment-listen", effectiveStringSettingValue(cfg.cluster?.enrollmentListen, ":9444")],
            ["--restore-backup", "true"],
            ["--restore-s3-access-key-id", effectiveStringSettingValue(backup.s3AccessKeyId, "$S3_ACCESS_KEY_ID")],
            ["--restore-s3-secret-access-key", "$S3_SECRET_ACCESS_KEY"],
            ["--restore-s3-bucket", effectiveStringSettingValue(backup.s3Bucket, "$S3_BUCKET")],
            ["--restore-s3-path", effectiveStringSettingValue(backup.s3Path, "opendeploy/primary")],
            ["--restore-s3-region", effectiveStringSettingValue(backup.s3Region, "us-east-1")],
            ["--recovery-code", "$RECOVERY_CODE"],
        ];
        const s3Endpoint = effectiveStringSettingValue(backup.s3Endpoint, "");
        if (s3Endpoint) args.splice(args.length - 1, 0, ["--restore-s3-endpoint", s3Endpoint]);
        const acmeHosts = effectiveStringSettingValue(httpsWeb.acmeHosts, "");
        if (acmeHosts) args.push(["--acme-hosts", acmeHosts]);
        const invocation = [
            "curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/restore_primary.sh | bash -s --",
            ...args.map(([flag, value]) => `  ${flag} ${shellQuote(value)}`),
        ].join(" \\\n");
        return [
            "# Set S3_SECRET_ACCESS_KEY and RECOVERY_CODE before running it.",
            "# OPENDEPLOY_VERSION defaults to the latest GitHub release; set it to vX.Y.Z to pin a version.",
            "",
            invocation,
        ].join("\n");
    };

    const generateMasterPassword = () => {
        const bytes = new Uint8Array(48);
        globalThis.crypto.getRandomValues(bytes);
        let binary = "";
        for (const b of bytes) binary += String.fromCharCode(b);
        newMasterPassword.val = btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
        newMasterPasswordRevealed.val = true;
        newMasterPasswordCopied.val = false;
    };

    const openMasterPasswordVerify = () => {
        masterPasswordVerifyValue.val = "";
        masterPasswordVerifyResult.val = "";
        masterPasswordVerifyOverlay.val = true;
    };
    const closeMasterPasswordVerify = () => {
        masterPasswordVerifyOverlay.val = false;
        masterPasswordVerifyValue.val = "";
    };
    const openMasterPasswordChange = () => {
        newMasterPassword.val = "";
        newMasterPasswordRevealed.val = false;
        newMasterPasswordCopied.val = false;
        masterPasswordOverlay.val = true;
    };
    const closeMasterPasswordChange = () => {
        masterPasswordOverlay.val = false;
        newMasterPassword.val = "";
        newMasterPasswordRevealed.val = false;
        newMasterPasswordCopied.val = false;
    };

    const saveNewMasterPassword = async () => {
        try {
            error.val = null;
            masterPasswordVerifyResult.val = "";
            await capi.postV1AuthMasterPasswordSave({password: newMasterPassword.val});
            closeMasterPasswordChange();
            masterPasswordVerifyValue.val = "";
            masterPasswordVerifyResult.val = "Master password updated.";
            masterPasswordVerifyOK.val = true;
        } catch (e) {
            error.val = e.message;
        }
    };

    const verifyMasterPassword = async () => {
        try {
            error.val = null;
            masterPasswordVerifyResult.val = "";
            await capi.postV1AuthMasterPasswordVerify({password: masterPasswordVerifyValue.val});
            masterPasswordVerifyResult.val = "Master password verified.";
            masterPasswordVerifyOK.val = true;
            closeMasterPasswordVerify();
        } catch (e) {
            masterPasswordVerifyResult.val = "Master password did not match.";
            masterPasswordVerifyOK.val = false;
        }
    };

    const copyNewMasterPassword = async () => {
        if (!newMasterPassword.val) return;
        await navigator.clipboard.writeText(newMasterPassword.val);
        newMasterPasswordCopied.val = true;
        setTimeout(() => { newMasterPasswordCopied.val = false; }, 1500);
    };

    const dialogHeader = (title, onClose) => div({class: "flex items-center justify-between gap-4"},
        h2({class: "text-base font-semibold"}, title),
        iconButton(closeIcon({class: "h-3.5 w-3.5"}), "Close", onClose));

    const onEscape = (close) => (e) => { if (e.key === "Escape") { e.preventDefault(); close(); } };

    const masterPasswordVerifyDialog = () => masterPasswordVerifyOverlay.val ? div(
        {class: overlayClass, onkeydown: onEscape(closeMasterPasswordVerify)},
        div({class: dialogClass, role: "dialog", "aria-modal": "true", "aria-label": "Verify master password"},
            dialogHeader("Verify master password", closeMasterPasswordVerify),
            p({class: "text-xs text-gray-400"}, "Enter the current master password to check it."),
            focusLater(input({
                class: "input w-full font-mono text-xs",
                type: "password",
                placeholder: "current master password",
                "aria-label": "Current master password",
                value: masterPasswordVerifyValue,
                oninput: (e) => { masterPasswordVerifyValue.val = e.target.value; masterPasswordVerifyResult.val = ""; },
                onkeydown: (e) => { if (e.key === "Enter" && masterPasswordVerifyValue.val.trim()) void verifyMasterPassword(); },
            })),
            () => masterPasswordVerifyResult.val
                ? p({class: () => `text-xs ${masterPasswordVerifyOK.val ? "text-green-400" : "text-red-400"}`}, masterPasswordVerifyResult.val)
                : "",
            div({class: "flex items-center justify-end gap-2"},
                button({type: "button", class: neutralButtonClass, onclick: closeMasterPasswordVerify}, "Cancel"),
                spinnerButton("Verify", verifyMasterPassword, primaryButtonClass, "button",
                    () => !masterPasswordVerifyValue.val.trim(), {base: smallButtonClass}),
            ),
        ),
    ) : "";

    const masterPasswordChangeDialog = () => masterPasswordOverlay.val ? div(
        {class: overlayClass, onkeydown: onEscape(closeMasterPasswordChange)},
        div({class: dialogClass, role: "dialog", "aria-modal": "true", "aria-label": "Change master password"},
            dialogHeader("Change master password", closeMasterPasswordChange),
            p({class: "text-xs text-gray-400"},
                "Type a new master password or generate one. Save it somewhere safe before submitting; it is not shown again."),
            div({class: "relative"},
                focusLater(input({
                    class: "input w-full font-mono text-xs",
                    style: "padding-right: 3.75rem",
                    type: () => newMasterPasswordRevealed.val ? "text" : "password",
                    placeholder: "new master password",
                    "aria-label": "New master password",
                    value: newMasterPassword,
                    oninput: (e) => { newMasterPassword.val = e.target.value; newMasterPasswordCopied.val = false; },
                })),
                div({class: "absolute right-1 top-1/2 flex -translate-y-1/2 items-center"},
                    iconButton(
                        () => newMasterPasswordCopied.val ? checkIcon({class: "h-3.5 w-3.5 text-green-400"}) : copyIcon({class: "h-3.5 w-3.5"}),
                        "Copy new master password", copyNewMasterPassword,
                        {disabled: () => !newMasterPassword.val}),
                    iconButton(
                        () => newMasterPasswordRevealed.val ? eyeOffIcon({class: "h-3.5 w-3.5"}) : eyeOpenIcon({class: "h-3.5 w-3.5"}),
                        "Reveal new master password", () => { newMasterPasswordRevealed.val = !newMasterPasswordRevealed.val; },
                        {disabled: () => !newMasterPassword.val}),
                ),
            ),
            div({class: "flex items-center justify-between gap-2"},
                button({type: "button", class: neutralButtonClass, onclick: generateMasterPassword}, "Generate"),
                div({class: "flex items-center gap-2"},
                    button({type: "button", class: neutralButtonClass, onclick: closeMasterPasswordChange}, "Cancel"),
                    spinnerButton("Save new password", saveNewMasterPassword, primaryButtonClass, "button",
                        () => !newMasterPassword.val, {base: smallButtonClass}),
                ),
            ),
        ),
    ) : "";

    const recoveryCodeDialog = () => recoveryCode.val ? div(
        {class: overlayClass},
        div({class: `${dialogClass} border-amber-600`, role: "dialog", "aria-modal": "true", "aria-label": "Save your recovery code", "data-testid": "recovery-code-dialog"},
            h2({class: "text-base font-semibold text-amber-400"}, "Save your recovery code"),
            p({class: "text-xs text-gray-400"},
                "This is shown only once and is not stored anywhere. Keep it somewhere safe: " +
                "it is the only way to recover secrets if this node is lost."),
            pre({class: "m-0 select-all whitespace-pre-wrap break-all rounded-md bg-code p-2.5 font-mono text-xs text-brand"},
                recoveryCode.val),
            div({class: "flex items-center justify-end gap-2"},
                button({type: "button", class: `${neutralButtonClass} inline-flex items-center gap-1.5`, onclick: copyRecoveryCode},
                    () => recoveryCodeCopied.val ? checkIcon({class: "h-3.5 w-3.5 text-green-400"}) : copyIcon({class: "h-3.5 w-3.5"}),
                    () => recoveryCodeCopied.val ? "Copied" : "Copy"),
                spinnerButton("I've saved it", async () => { recoveryCode.val = ""; }, primaryButtonClass, "button", undefined, {base: smallButtonClass}),
            ),
        ),
    ) : "";

    const createSecretDialog = () => {
        const target = createSecretTarget.val;
        if (!target) return "";
        return valueOverlay({
            mode: "create",
            type: "secret",
            name: target.name,
            onSave: saveCreatedSecret,
            onClose: closeCreateSecret,
        });
    };

    const editSecretDialog = () => {
        const target = editSecretTarget.val;
        if (!target) return "";
        return valueOverlay({
            type: "secret",
            name: target.name,
            value: target.value,
            version: target.version,
            createdAt: target.createdAt,
            onSave: saveEditedSecret,
            onClose: closeEditSecret,
        });
    };

    const query = () => search.val.trim().toLowerCase();
    const matchesSearch = (section, text) => {
        const q = query();
        return !q || text.toLowerCase().includes(q) || section.title.toLowerCase().includes(q);
    };

    const settingVisible = (section, setting) => {
        if (setting.visible && !setting.visible(draft.val)) return false;
        if (section.key === "web" && ["ACME_HOSTS", "ACME_EMAIL"].includes(setting.key)) {
            return !effectiveDraftBoolValue(draft.val?.WEB_TLS_SELF_MANAGED);
        }
        return !section.enabledKey
            || setting.key === section.enabledKey
            || effectiveDraftBoolValue(draft.val?.[section.enabledKey]);
    };
    const settingShown = (section, setting) => settingVisible(section, setting) && matchesSearch(section, setting.label);

    const labelCell = (text, dirty = () => false) => td({class: `${cellClass} overflow-hidden whitespace-nowrap`},
        div({class: "flex min-w-0 items-center gap-1.5"},
            span({class: () => `h-1.5 w-1.5 flex-none rounded-full ${dirty() ? "bg-amber-300" : "invisible"}`}),
            span({class: "min-w-0 truncate text-gray-300"}, text)));

    const sourceCell = (setting, item, patch) => td({class: cellClass},
        settingUsesConfigRef(setting)
            ? select({
                class: "input h-6 w-full cursor-pointer text-[11px]",
                "aria-label": `${setting.label} source`,
                disabled: () => saving.val,
                value: () => item()?.mode || "value",
                onchange: (e) => patch({mode: e.target.value}),
            },
            option({value: "value", selected: () => (item()?.mode || "value") === "value"}, "Value"),
            option({value: "config", selected: () => item()?.mode === "config"}, "Config"))
            : span({class: "inline-flex h-6 items-center gap-1 px-1 text-[11px] text-gray-500"},
                secretKeyIcon({class: "h-3 w-3 text-purple-300"}), "Secret"));

    const configPicker = (setting, item, patch) => referencePicker({
        refs: () => latestRefs(userConfigRefsS.val || [], item()?.configRefID || 0),
        selectedKey: () => item()?.configRefID || "",
        selectedLabel: "",
        getKey: (ref) => ref.id,
        getLabel: refLabel,
        placeholder: "Search configs",
        noMatchesLabel: "No matching configs",
        emptyLabel: "No configs available",
        inputClass: controlClass,
        containerClass: "relative w-full min-w-0",
        disabled: () => saving.val,
        onSelect: (ref) => patch({configRefID: ref.id}),
    });

    const textControl = (setting, item, patch) => input({
        class: controlClass,
        type: "text",
        "aria-label": setting.label,
        disabled: () => saving.val,
        value: () => item()?.value || "",
        oninput: (e) => patch({value: e.target.value}),
    });

    const boolControl = (setting, item, patch) => {
        const on = () => item()?.value === "true";
        return labelEl({class: "inline-flex h-6 cursor-pointer select-none items-center gap-2"},
            span({class: "relative h-4 w-7 flex-none"},
                input({
                    class: "peer absolute inset-0 m-0 h-full w-full cursor-pointer opacity-0",
                    type: "checkbox",
                    "aria-label": setting.label,
                    disabled: () => saving.val,
                    checked: on,
                    onchange: (e) => patch({value: e.target.checked ? "true" : "false"}),
                }),
                span({
                    class: () => `pointer-events-none absolute inset-0 rounded-full transition-colors peer-focus-visible:ring-1 peer-focus-visible:ring-brand ${
                        on() ? "bg-brand" : "bg-gray-600"
                    } before:absolute before:left-0.5 before:top-0.5 before:h-3 before:w-3 before:rounded-full before:bg-white before:transition-transform ${
                        on() ? "before:translate-x-3" : ""}`,
                })),
            span({class: "w-8 font-mono text-[11px] text-gray-400"}, () => on() ? "true" : "false"));
    };

    const secretControl = (setting, item, patch) => div({class: "flex min-w-0 items-center gap-0.5"},
        div({class: "mr-1 w-[26rem] max-w-full min-w-0"}, referencePicker({
            refs: () => latestRefs(secretRefsS.val || [], item()?.secretId || 0),
            selectedKey: () => item()?.secretId || "",
            selectedLabel: "",
            getKey: (ref) => ref.id,
            getLabel: refLabel,
            placeholder: "Search secrets",
            noMatchesLabel: "No matching secrets",
            emptyLabel: "No secrets available",
            inputClass: controlClass,
            containerClass: "relative w-full min-w-0",
            disabled: () => saving.val,
            onSelect: (ref) => patch({secretId: ref.id}),
        })),
        iconButton(eyeOpenIcon({class: "h-3.5 w-3.5"}), "Open secret editor", () => { void openEditSecret(setting); },
            {disabled: () => saving.val || !item()?.secretId || openingSecretID.val === Number(item()?.secretId || 0)}),
        iconButton(closeIcon({class: "h-3.5 w-3.5"}), "Clear secret", () => patch({secretId: 0}),
            {disabled: () => saving.val || !item()?.secretId}),
        iconButton(plusIcon({class: "h-3.5 w-3.5"}), "Create secret", () => openCreateSecret(setting),
            {disabled: () => saving.val}));

    // The value/config choice is a child binding keyed on a derived mode:
    // switching re-renders the control, while typing never rebuilds it.
    const control = (setting, item, patch) => {
        if (setting.type === "secret") return secretControl(setting, item, patch);
        const modeS = van.derive(() => item()?.mode || "value");
        return div(
            {class: () => modeS.val === "config" || setting.type !== "bool" ? "w-[26rem] max-w-full min-w-0" : "flex-none"},
            () => modeS.val === "config"
                ? configPicker(setting, item, patch)
                : setting.type === "bool" ? boolControl(setting, item, patch) : textControl(setting, item, patch),
        );
    };

    const valueCell = (setting, item, patch) => td({class: cellClass},
        div({class: "flex min-w-0 items-center gap-3"},
            control(setting, item, patch),
            span({class: hintClass}, () => rowHint(setting, item()))));

    const settingRow = (section, setting) => {
        const item = () => draft.val?.[setting.key];
        const patch = (next) => patchDraft(setting.key, next);
        const dirty = () => { const it = item(); return Boolean(it && isDirty(setting, it)); };
        return tr(
            {
                "data-testid": `setting-row-${setting.key}`,
                class: () => !settingShown(section, setting) ? "hidden" : dirty() ? "bg-amber-500/5" : "hover:bg-gray-700/25",
            },
            labelCell(setting.label, dirty),
            sourceCell(setting, item, patch),
            valueCell(setting, item, patch),
        );
    };

    const actionRow = (section, key, label, content, {top = false} = {}) => tr(
        {
            "data-testid": `setting-row-${key}`,
            class: () => matchesSearch(section, label) ? "hover:bg-gray-700/25" : "hidden",
        },
        top ? td({class: `${cellClass} overflow-hidden whitespace-nowrap align-top pt-2.5`},
            div({class: "flex min-w-0 items-center gap-1.5"},
                span({class: "h-1.5 w-1.5 flex-none rounded-full invisible"}),
                span({class: "min-w-0 truncate text-gray-300"}, label)))
            : labelCell(label),
        td({class: cellClass, colspan: 2}, content),
    );

    const masterPasswordRow = div({class: "flex min-w-0 flex-wrap items-center gap-2"},
        button({type: "button", class: rowButtonClass, onclick: openMasterPasswordVerify}, "Verify…"),
        button({type: "button", class: rowButtonClass, onclick: openMasterPasswordChange}, "Change…"),
        () => masterPasswordVerifyResult.val
            ? span({class: () => `text-[11px] ${masterPasswordVerifyOK.val ? "text-green-400" : "text-red-400"}`}, masterPasswordVerifyResult.val)
            : span({class: mutedClass}, "Registers passkeys at bootstrap and recovery, and signs in when master password login is on."));

    const recoveryCodeRow = () => {
        const status = secretsStatusS.val;
        if (!status) return span({class: mutedClass}, "Loading…");
        if (!status.unlocked) {
            return span({class: "text-[11px] text-amber-300"}, "Unlock the secrets store on the Secrets page before managing the recovery code.");
        }
        const configured = Boolean(status.recoveryConfigured);
        return div({class: "flex min-w-0 flex-wrap items-center gap-2"},
            spinnerButton(configured ? "Regenerate recovery code" : "Generate recovery code", generateRecovery,
                configured ? "border-gray-600 text-gray-300 hover:bg-surface-hover" : "border-brand bg-brand text-white hover:bg-blue-600",
                "button", undefined, {base: rowButtonBase}),
            configured
                ? span({class: "inline-flex items-center gap-1 text-[11px] text-green-400"}, checkIcon({class: "h-3 w-3"}), "A recovery code is configured.")
                : span({class: "text-[11px] text-amber-300"}, "No recovery code. Generate one so secrets can be recovered if this node is lost."));
    };

    const restoreCommandRow = div({class: "flex max-w-4xl flex-col gap-1.5 py-1"},
        p({class: mutedClass}, "Restores this primary on a new node from its S3 backup, with the current settings filled in. Set the placeholders before running."),
        codeBlock({
            title: "restore_primary.sh",
            value: recoveryInstallExample,
            open: restoreCommandOpen,
            wrap: true,
            lineNumbers: false,
            testId: "restore-command",
        }));

    const settingsTable = (...rows) => table(
        {class: "w-full table-fixed border-separate border-spacing-0 text-xs"},
        colgroup(col({style: "width:16rem"}), col({style: "width:6.5rem"}), col()),
        tbody(...rows),
    );

    const groupEl = (section, rows, shownCount, ruled) => div(
        {class: () => `${shownCount() ? "flex" : "hidden"}${ruled() ? " border-t border-gray-700" : ""}`, "data-testid": `settings-section-${section.key}`},
        div({class: "flex w-[7.5rem] flex-none items-center justify-center border-r border-gray-800/80 px-2 py-1.5 text-center"},
            h2({class: "text-[10.5px] font-semibold uppercase leading-tight tracking-wider text-gray-400"}, section.title)),
        div({class: "min-w-0 flex-1 py-1.5"}, settingsTable(...rows)),
    );

    const extraRows = {
        auth: [{key: "MASTER_PASSWORD", label: "Master password", content: masterPasswordRow}],
    };
    const groups = settingsSections.map((section) => {
        const extras = extraRows[section.key] || [];
        return {
            section,
            rows: [
                ...section.settings.map((s) => settingRow(section, s)),
                ...extras.map((r) => actionRow(section, r.key, r.label, r.content)),
            ],
            shownCount: () => section.settings.filter((s) => settingShown(section, s)).length
                + extras.filter((r) => matchesSearch(section, r.label)).length,
        };
    });
    const recoverySection = {key: "recovery", title: "Secrets recovery"};
    const recoveryRows = [["RECOVERY_CODE", "Recovery code", recoveryCodeRow, {}], ["RESTORE_COMMAND", "Restore command", restoreCommandRow, {top: true}]];
    groups.push({
        section: recoverySection,
        rows: recoveryRows.map(([key, label, content, opts]) => actionRow(recoverySection, key, label, content, opts)),
        shownCount: () => recoveryRows.filter(([, label]) => matchesSearch(recoverySection, label)).length,
    });
    const firstShown = () => groups.findIndex((group) => group.shownCount() > 0);
    const sections = groups.map((group, index) => groupEl(group.section, group.rows, group.shownCount, () => index > firstShown()));

    const searchBox = div({class: "relative"},
        searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
        input({
            class: "text-input search-input search-input-iconed toolbar-input",
            type: "search",
            placeholder: "Search settings",
            "aria-label": "Search settings",
            "data-testid": "settings-search",
            value: search,
            oninput: (e) => { search.val = e.target.value; },
        }));

    const saveControls = () => !dirtyCount.val ? "" : div({class: "flex items-center gap-2"},
        span({class: "inline-flex items-center gap-1.5 text-xs text-amber-300"},
            span({class: "h-1.5 w-1.5 rounded-full bg-amber-300"}),
            "Unsaved changes",
            span({class: "tabular-nums text-amber-200/70"}, () => String(dirtyCount.val))),
        button({type: "button", class: "toolbar-button", disabled: () => saving.val, onclick: resetChanges}, "Reset"),
        spinnerButton("Save changes", saveChanges, "border border-brand bg-brand text-white hover:bg-blue-600", "button",
            () => saving.val, {base: "inline-flex h-[30px] items-center rounded-lg px-3 text-xs font-medium whitespace-nowrap"}));

    const toolbar = div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        searchBox,
        div({class: "flex-1"}),
        saveControls,
    );

    const statusLine = () => settingsChangedElsewhere.val
        ? div({class: "flex flex-none items-center gap-1.5 border-b border-amber-500/30 bg-amber-500/10 px-2 py-1 text-[11px] text-amber-200"},
            infoIcon({class: "h-3 w-3 flex-none"}),
            "Settings changed elsewhere while you were editing. Saving overwrites those changes; Reset loads them and drops yours.")
        : div({class: "flex flex-none items-center gap-1.5 border-b border-gray-800 bg-gray-950/40 px-2 py-1 text-[11px] text-gray-500"},
            infoIcon({class: "h-3 w-3 flex-none"}),
            "A setting holds a value typed here or follows a config from Secrets / Configs; secrets are always references. Nothing applies until saved.");

    const body = div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto"},
        () => loaded.val ? "" : p({class: "px-3 py-3 text-xs text-gray-500"}, "Loading…"),
        div({class: () => loaded.val ? "flex flex-col" : "hidden"}, ...sections),
    );

    return div(
        {class: "flex h-full min-h-0 min-w-0 flex-col overflow-hidden bg-surface", "data-testid": "settings-page"},
        toolbar,
        () => error.val ? p(
            {class: "flex-none border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"},
            `Error: ${error.val}`) : "",
        statusLine,
        body,
        masterPasswordVerifyDialog,
        masterPasswordChangeDialog,
        recoveryCodeDialog,
        createSecretDialog,
        editSecretDialog,
    );
}
