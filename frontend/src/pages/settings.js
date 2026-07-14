import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {referencePicker} from "../components/referencePicker.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {copyIcon, eyeOffIcon, eyeOpenIcon} from "../lib/icons.js";
import {secretRefsS, spacesS, userConfigRefsS, userConfigsS} from "../state/deployments.js";

const { div, h2, p, pre, span, table, tbody, tr, td, button, code, input, select, option, label: labelEl } = van.tags;

const boolValue = (value) => value ? "true" : "false";
const shellQuote = (value) => {
    const s = String(value ?? "");
    if (!s) return "''";
    if (/^\$[A-Za-z_][A-Za-z0-9_]*$/.test(s)) return s;
    if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s;
    return `'${s.replace(/'/g, `'"'"'`)}'`;
};

const refID = (ref) => Number(ref?.id || 0);
const deepClone = (value) => JSON.parse(JSON.stringify(value));
const stringSetting = (value = "") => ({value, configRef: undefined});
const boolSetting = (value = false) => ({value, configRef: undefined});
const secretSetting = (id = 0) => (id ? {id} : {});
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
const configRefPayload = (item) => ({id: Number(item.configRefID || 0)});
const secretRefPayload = (item) => ({id: Number(item.secretId || 0)});
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
    },
});

const resolvedConfigValue = (id) => {
    id = Number(id || 0);
    if (!id) return "";
    const item = (userConfigsS.val || []).find(cfg => Number(cfg.id || 0) === id);
    return item?.value || "";
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
        title: "Web UI",
        settings: [
            {label: "Web UI HTTPS enabled", key: "WEB_HTTPS_ENABLED", type: "bool", setting: (cfg) => cfg.httpsWeb?.enabled, apply: (doc, item) => { doc.httpsWeb.enabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI HTTPS listen", key: "WEB_HTTPS_LISTEN", type: "text", setting: (cfg) => cfg.httpsWeb?.listen, apply: (doc, item) => { doc.httpsWeb.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI use self managed TLS cert", key: "WEB_TLS_SELF_MANAGED", type: "bool", setting: (cfg) => cfg.httpsWeb?.tlsSelfManaged, apply: (doc, item) => { doc.httpsWeb.tlsSelfManaged = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI TLS cert PEM", key: "WEB_TLS_CERT_PEM", type: "secret", secret: (cfg) => cfg.httpsWeb?.tlsCertPem, apply: (doc, item) => { doc.httpsWeb.tlsCertPem = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.web_tls_cert_pem", visible: (draft) => effectiveDraftBoolValue(draft?.WEB_TLS_SELF_MANAGED)},
            {label: "Web UI ACME hosts", key: "ACME_HOSTS", type: "text", setting: (cfg) => cfg.httpsWeb?.acmeHosts, apply: (doc, item) => { doc.httpsWeb.acmeHosts = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI ACME email", key: "ACME_EMAIL", type: "text", setting: (cfg) => cfg.httpsWeb?.acmeEmail, apply: (doc, item) => { doc.httpsWeb.acmeEmail = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Web UI HTTP enabled", key: "WEB_HTTP_ENABLED", type: "bool", setting: (cfg) => cfg.httpWeb?.enabled, apply: (doc, item) => { doc.httpWeb.enabled = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value === "true"}; }},
            {label: "Web UI HTTP listen", key: "WEB_HTTP_LISTEN", type: "text", setting: (cfg) => cfg.httpWeb?.listen, apply: (doc, item) => { doc.httpWeb.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
        ],
    },
    {
        title: "Cluster details",
        settings: [
            {label: "Cluster listen", key: "CLUSTER_LISTEN", type: "text", setting: (cfg) => cfg.cluster?.listen, apply: (doc, item) => { doc.cluster.listen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
            {label: "Cluster enrollment listen", key: "ENROLLMENT_LISTEN", type: "text", setting: (cfg) => cfg.cluster?.enrollmentListen, apply: (doc, item) => { doc.cluster.enrollmentListen = item.mode === "config" ? {configRef: configRefPayload(item)} : {value: item.value}; }},
        ],
    },
    {
        title: "Repository credentials",
        settings: [
            {label: "GitHub token", key: "GITHUB_TOKEN", type: "secret", secret: (cfg) => cfg.repo?.githubToken, apply: (doc, item) => { doc.repo.githubToken = item.secretId ? secretRefPayload(item) : {}; }, defaultSecretName: "opendeploy.config.github_token"},
        ],
    },
    {
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
            cleared: false,
            revealed: false,
            revealedValue: "",
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

const inputClass = "w-full min-w-64 rounded-sm bg-gray-800 border border-gray-700 px-1.5 py-1 text-xs text-gray-100 " +
    "focus:outline-none focus:ring-1 focus:ring-brand";
const compactButtonClass = "h-8 px-3 py-1 rounded-md text-sm leading-none";
const defaultSpaceIDs = new Set([0, 1]);
let settingsPageNode = null;

function valueInput(setting, draft, error, patchDraft, saving, secrets, openCreateSecret) {
    const item = () => draft.val?.[setting.key];
    const patch = (next) => patchDraft(setting.key, next);
    const mode = () => item()?.mode || "value";
    const modeSelector = settingUsesConfigRef(setting)
        ? select({
            class: "w-20 rounded-sm bg-gray-800 border border-gray-700 px-1.5 py-1 text-xs text-gray-100 focus:outline-none focus:ring-1 focus:ring-brand",
            disabled: () => saving.val,
            value: () => mode(),
            onchange: (e) => patch({mode: e.target.value}),
        },
        option({value: "value"}, "value"),
        option({value: "config"}, "config"))
        : "";

    const configPicker = () => div(
        {class: "flex items-center gap-1.5 w-full"},
        modeSelector,
        referencePicker({
            refs: () => latestRefs(userConfigRefsS.val || [], item()?.configRefID || 0),
            selectedKey: () => item()?.configRefID || "",
            selectedLabel: "",
            getKey: ref => ref.id,
            getLabel: refLabel,
            placeholder: "Search configs",
            noMatchesLabel: "No matching configs",
            emptyLabel: "No configs available",
            inputClass,
            containerClass: "relative min-w-64 flex-1",
            disabled: () => saving.val,
            onSelect: ref => patch({configRefID: ref.id}),
        }),
    );

    if (setting.type === "bool") {
        if (settingUsesConfigRef(setting) && mode() === "config") return configPicker();
        return div(
            {class: "inline-flex items-center gap-2"},
            modeSelector,
            labelEl(
                {class: "inline-flex items-center gap-2 cursor-pointer select-none"},
                input({
                    class: "sr-only peer",
                    type: "checkbox",
                    disabled: () => saving.val,
                    checked: () => item()?.value === "true",
                    onchange: (e) => patch({value: e.target.checked ? "true" : "false"}),
                }),
                span({
                    class: () => `relative h-5 w-9 rounded-full transition-colors ${
                        item()?.value === "true" ? "bg-brand" : "bg-gray-700"
                    } before:absolute before:left-0.5 before:top-0.5 before:h-4 before:w-4 before:rounded-full ` +
                        `before:bg-white before:transition-transform ${item()?.value === "true" ? "before:translate-x-4" : ""}`,
                }),
                span({class: "text-xs text-gray-300"}, () => item()?.value === "true" ? "true" : "false"),
            ),
        );
    }
    if (setting.type === "secret") {
        const revealSecret = async () => {
            const current = item();
            if (!current?.secretId) return;
            if (current.revealed) { patch({revealed: false, revealedValue: ""}); return; }
            try {
                error.val = null;
                const res = await capi.postV1SecretsReveal({id: current.secretId});
                patch({revealed: true, revealedValue: new TextDecoder().decode(res.value)});
            } catch (e) {
                error.val = e.message;
            }
        };
        return div(
            {class: "flex flex-wrap items-center gap-1.5"},
            referencePicker({
                refs: () => latestRefs(secretRefsS.val?.length ? secretRefsS.val : secrets.val, item()?.secretId || 0),
                selectedKey: () => item()?.secretId || "",
                selectedLabel: "",
                getKey: ref => ref.id,
                getLabel: refLabel,
                placeholder: "Search secrets",
                noMatchesLabel: "No matching secrets",
                emptyLabel: "No secrets available",
                inputClass,
                containerClass: "relative min-w-64 flex-1",
                disabled: () => saving.val,
                onSelect: ref => patch({secretId: ref.id, revealed: false, revealedValue: ""}),
            }),
            button({
                type: "button",
                disabled: () => saving.val,
                class: "text-xs px-2 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer whitespace-nowrap",
                onclick: () => openCreateSecret(setting),
            }, "Create secret"),
            () => item()?.secretId ? button({
                type: "button",
                title: () => item().revealed ? "Hide saved secret" : "Reveal saved secret",
                "aria-label": () => item().revealed ? "Hide saved secret" : "Reveal saved secret",
                disabled: () => saving.val,
                class: "p-1 rounded text-gray-300 bg-gray-700 hover:bg-gray-600 cursor-pointer",
                onclick: revealSecret,
            }, () => item().revealed ? eyeOffIcon() : eyeOpenIcon()) : "",
            () => item()?.secretId ? button({
                type: "button",
                disabled: () => saving.val,
                class: "text-xs px-2 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer whitespace-nowrap",
                onclick: () => patch({secretId: 0, revealed: false, revealedValue: ""}),
            }, "Clear") : "",
            () => item()?.revealed ? code({
                class: "text-xs text-amber-200 bg-amber-950/40 px-2 py-1 rounded truncate max-w-64",
                title: item().revealedValue,
            }, item().revealedValue) : "",
        );
    }
    if (settingUsesConfigRef(setting) && mode() === "config") return configPicker();
    return div(
        {class: "flex items-center gap-1.5 w-full"},
        modeSelector,
        input({
            class: inputClass,
            disabled: () => saving.val,
            value: () => item()?.value || "",
            oninput: (e) => patch({value: e.target.value}),
        }),
    );
}

export function settingsPage() {
    if (settingsPageNode) return settingsPageNode;

    const config = van.state(null);
    const draft = van.state(null);
    const dirtyCount = van.state(0);
    const loaded = van.state(false);
    const error = van.state(null);
    const saving = van.state(false);
    const secrets = van.state([]);
    const recoveryStatus = van.state(null);
    const recoveryCode = van.state("");
    const recoveryExampleOpen = van.state(false);
    const masterPasswordVerifyValue = van.state("");
    const masterPasswordVerifyResult = van.state("");
    const masterPasswordVerifyOK = van.state(false);
    const masterPasswordVerifyOverlay = van.state(false);
    const masterPasswordOverlay = van.state(false);
    const newMasterPassword = van.state("");
    const newMasterPasswordRevealed = van.state(false);
    const newMasterPasswordCopied = van.state(false);
    const createSecretOverlay = van.state(false);
    const createSecretSettingKey = van.state("");
    const createSecretName = van.state("");
    const createSecretValue = van.state("");
    const createSecretRevealed = van.state(false);
    const editingSpaceID = van.state(null);
    const editingSpaceName = van.state("");
    const addingSpace = van.state(false);
    const newSpaceName = van.state("");
    const spaceSaving = van.state(false);

    const setDraft = (next) => {
        draft.val = next;
        dirtyCount.val = dirtySettingsFor(next).length;
    };

    const patchDraft = (key, next) => {
        const current = draft.val;
        if (!current?.[key]) return;
        setDraft({...current, [key]: {...current[key], ...next}});
    };

    const load = async () => {
        try {
            error.val = null;
            const [cfg, secretsStatus] = await Promise.all([
                capi.getV1Settings(),
                capi.postV1SecretsStatus({}),
            ]);
            config.val = cfg;
            recoveryStatus.val = secretsStatus;
            secrets.val = secretsStatus.unlocked ? (await capi.postV1SecretsList({})).items || [] : [];
            setDraft(configDraft(config.val));
            loaded.val = true;
        } catch (e) {
            error.val = e.message;
        }
    };
    setTimeout(load, 0);

    const dirtySettings = () => dirtySettingsFor(draft.val);

    const resetChanges = () => {
        if (!config.val) return;
        setDraft(configDraft(config.val));
    };

    const reloadSecrets = async () => {
        recoveryStatus.val = await capi.postV1SecretsStatus({});
        secrets.val = recoveryStatus.val.unlocked ? (await capi.postV1SecretsList({})).items || [] : [];
    };

    const openCreateSecret = (setting) => {
        createSecretSettingKey.val = setting.key;
        createSecretName.val = setting.defaultSecretName || "";
        createSecretValue.val = "";
        createSecretRevealed.val = false;
        createSecretOverlay.val = true;
    };

    const closeCreateSecret = () => {
        createSecretOverlay.val = false;
        createSecretSettingKey.val = "";
        createSecretName.val = "";
        createSecretValue.val = "";
        createSecretRevealed.val = false;
    };

    const saveCreatedSecret = async () => {
        try {
            error.val = null;
            const name = createSecretName.val.trim();
            const saved = await capi.postV1SecretsSet({
                name,
                value: new TextEncoder().encode(createSecretValue.val),
            });
            await reloadSecrets();
            patchDraft(createSecretSettingKey.val, {secretId: saved.id, revealed: false, revealedValue: ""});
            closeCreateSecret();
        } catch (e) {
            error.val = e.message;
        }
    };

    const saveChanges = async () => {
        if (saving.val) return;
        try {
            saving.val = true;
            error.val = null;
            const payload = deepClone(config.val || emptySettings());
            dirtySettings().forEach(({setting, item}) => setting.apply(payload, item));
            const res = await capi.putV1Settings(payload);
            config.val = res;
            setDraft(configDraft(res));
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    const generateRecovery = async () => {
        try {
            error.val = null;
            const res = await capi.postV1SecretsGenerateRecoveryCode({});
            recoveryCode.val = res.code;
            recoveryStatus.val = await capi.postV1SecretsStatus({});
        } catch (e) {
            error.val = e.message;
        }
    };

	const recoveryInstallExample = () => {
		const cfg = config.val || {};
		const httpWeb = cfg.httpWeb || {};
		const httpsWeb = cfg.httpsWeb || {};
		const backup = cfg.backup || {};
		const httpOnly = effectiveBoolSettingValue(httpWeb.enabled, false) && !effectiveBoolSettingValue(httpsWeb.enabled, true);
		const webListen = httpOnly
			? effectiveStringSettingValue(httpWeb.listen, ":8080")
			: effectiveStringSettingValue(httpsWeb.listen, ":443");
        const args = [
            ["--http-only", boolValue(httpOnly)],
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

    const saveNewMasterPassword = async () => {
        try {
            error.val = null;
            masterPasswordVerifyResult.val = "";
            await capi.postV1AuthMasterPasswordSave({password: newMasterPassword.val});
            masterPasswordOverlay.val = false;
            newMasterPassword.val = "";
            newMasterPasswordRevealed.val = false;
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
            masterPasswordVerifyOverlay.val = false;
            masterPasswordVerifyValue.val = "";
        } catch (e) {
            masterPasswordVerifyResult.val = "Master password did not match.";
            masterPasswordVerifyOK.val = false;
        }
    };

    const resetSpaceDraft = () => {
        editingSpaceID.val = null;
        editingSpaceName.val = "";
        addingSpace.val = false;
        newSpaceName.val = "";
    };

    const isDefaultSpace = (space) => defaultSpaceIDs.has(Number(space?.id));

    const startRenameSpace = (space) => {
        if (isDefaultSpace(space) || spaceSaving.val) return;
        addingSpace.val = false;
        newSpaceName.val = "";
        editingSpaceID.val = space.id;
        editingSpaceName.val = space.name || "";
    };

    const saveRenamedSpace = async (space) => {
        const name = editingSpaceName.val.trim();
        if (!name || isDefaultSpace(space) || spaceSaving.val) return;
        if (name === (space.name || "")) {
            resetSpaceDraft();
            return;
        }
        try {
            spaceSaving.val = true;
            error.val = null;
            await capi.postV1SpacesUpdate({id: space.id, name});
            resetSpaceDraft();
        } catch (e) {
            error.val = e.message;
        } finally {
            spaceSaving.val = false;
        }
    };

    const removeSpace = async (space) => {
        if (isDefaultSpace(space) || spaceSaving.val) return;
        try {
            spaceSaving.val = true;
            error.val = null;
            await capi.postV1SpacesDelete({id: space.id});
            if (editingSpaceID.val === space.id) resetSpaceDraft();
        } catch (e) {
            error.val = e.message;
        } finally {
            spaceSaving.val = false;
        }
    };

    const saveNewSpace = async () => {
        const name = newSpaceName.val.trim();
        if (!name || spaceSaving.val) return;
        try {
            spaceSaving.val = true;
            error.val = null;
            await capi.postV1SpacesCreate({name});
            resetSpaceDraft();
        } catch (e) {
            error.val = e.message;
        } finally {
            spaceSaving.val = false;
        }
    };

    const copyNewMasterPassword = async () => {
        if (!newMasterPassword.val) return;
        await navigator.clipboard.writeText(newMasterPassword.val);
        newMasterPasswordCopied.val = true;
        setTimeout(() => { newMasterPasswordCopied.val = false; }, 1500);
    };

    const masterPasswordCard = () => {
        return div(
            {class: "card w-full flex flex-col gap-2"},
            div({class: "flex items-center justify-between gap-4"},
                div({class: "flex flex-col gap-1"},
                    h2({class: "text-base font-semibold"}, "Master password"),
                    () => masterPasswordVerifyResult.val
                        ? p({class: () => `text-xs ${masterPasswordVerifyOK.val ? "text-green-400" : "text-red-400"}`}, masterPasswordVerifyResult.val)
                        : p({class: "text-xs text-gray-400"},
                            "Verify the current master password or update it."),
                ),
                div({class: "flex items-center gap-2"},
                    button({
                        type: "button",
                        class: `${compactButtonClass} whitespace-nowrap bg-gray-700 text-gray-200 hover:bg-gray-600`,
                        onclick: () => { masterPasswordVerifyOverlay.val = true; masterPasswordVerifyValue.val = ""; masterPasswordVerifyResult.val = ""; },
                    }, "Verify master password"),
                    button({
                        type: "button",
                        class: `${compactButtonClass} whitespace-nowrap bg-gray-700 text-gray-200 hover:bg-gray-600`,
                        onclick: () => { masterPasswordOverlay.val = true; newMasterPassword.val = ""; newMasterPasswordRevealed.val = false; newMasterPasswordCopied.val = false; },
                    }, "Update master password"),
                ),
            ),
        );
    };

    const masterPasswordVerifyDialog = () => masterPasswordVerifyOverlay.val ? div(
        {class: "fixed inset-0 z-50 bg-black/60 flex items-center justify-center p-6"},
        div({class: "card w-full max-w-xl flex flex-col gap-3 border-gray-600"},
            div({class: "flex items-center justify-between gap-4"},
                h2({class: "text-base font-semibold"}, "Verify master password"),
                button({
                    type: "button",
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: () => { masterPasswordVerifyOverlay.val = false; masterPasswordVerifyValue.val = ""; },
                }, "Close"),
            ),
            p({class: "text-xs text-gray-400"}, "Enter the current master password to verify it."),
            input({
                class: `${inputClass} font-mono`,
                type: "password",
                placeholder: "current master password",
                value: masterPasswordVerifyValue,
                oninput: (e) => { masterPasswordVerifyValue.val = e.target.value; masterPasswordVerifyResult.val = ""; },
            }),
            () => masterPasswordVerifyResult.val
                ? p({class: () => `text-xs ${masterPasswordVerifyOK.val ? "text-green-400" : "text-red-400"}`}, masterPasswordVerifyResult.val)
                : "",
            div({class: "flex items-center justify-end gap-2"},
                button({
                    type: "button",
                    class: `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
                    onclick: () => { masterPasswordVerifyOverlay.val = false; masterPasswordVerifyValue.val = ""; },
                }, "Cancel"),
                spinnerButton("Verify master password", verifyMasterPassword,
                    `${compactButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
                    "button", () => !masterPasswordVerifyValue.val.trim()),
            ),
        ),
    ) : "";

    const masterPasswordUpdateOverlay = () => masterPasswordOverlay.val ? div(
        {class: "fixed inset-0 z-50 bg-black/60 flex items-center justify-center p-6"},
        div({class: "card w-full max-w-xl flex flex-col gap-3 border-gray-600"},
            div({class: "flex items-center justify-between gap-4"},
                h2({class: "text-base font-semibold"}, "Update master password"),
                button({
                    type: "button",
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: () => { masterPasswordOverlay.val = false; newMasterPassword.val = ""; newMasterPasswordRevealed.val = false; newMasterPasswordCopied.val = false; },
                }, "Close"),
            ),
            p({class: "text-xs text-gray-400"},
                "Enter a new master password or generate one here. Save it before submitting; it will not be shown again."),
            div({class: "relative"},
                input({
                    class: `${inputClass} pr-20 font-mono`,
                    type: () => newMasterPasswordRevealed.val ? "text" : "password",
                    placeholder: "new master password",
                    value: newMasterPassword,
                    oninput: (e) => { newMasterPassword.val = e.target.value; newMasterPasswordCopied.val = false; },
                }),
                button({
                    type: "button",
                    title: "Copy new master password",
                    "aria-label": "Copy new master password",
                    disabled: () => !newMasterPassword.val,
                    class: () => `absolute right-9 top-1/2 -translate-y-1/2 p-1.5 rounded text-gray-300 ` +
                        `hover:bg-gray-700 cursor-pointer ${newMasterPassword.val ? "" : "invisible"}`,
                    onclick: copyNewMasterPassword,
                }, copyIcon()),
                () => newMasterPasswordCopied.val ? span({
                    class: "absolute right-20 top-1/2 -translate-y-1/2 text-xs text-green-400 bg-gray-900 px-2 py-1 rounded",
                }, "Copied") : "",
                button({
                    type: "button",
                    title: () => newMasterPasswordRevealed.val ? "Hide new master password" : "Reveal new master password",
                    "aria-label": () => newMasterPasswordRevealed.val ? "Hide new master password" : "Reveal new master password",
                    disabled: () => !newMasterPassword.val,
                    class: () => `absolute right-1 top-1/2 -translate-y-1/2 p-1.5 rounded text-gray-300 ` +
                        `hover:bg-gray-700 cursor-pointer ${newMasterPassword.val ? "" : "invisible"}`,
                    onclick: () => { newMasterPasswordRevealed.val = !newMasterPasswordRevealed.val; },
                }, () => newMasterPasswordRevealed.val ? eyeOffIcon() : eyeOpenIcon()),
            ),
            div({class: "flex items-center justify-between gap-2"},
                button({
                    type: "button",
                    class: `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
                    onclick: generateMasterPassword,
                }, "Generate password"),
                div({class: "flex items-center gap-2"},
                    button({
                        type: "button",
                        class: `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
                        onclick: () => { masterPasswordOverlay.val = false; newMasterPassword.val = ""; newMasterPasswordRevealed.val = false; newMasterPasswordCopied.val = false; },
                    }, "Cancel"),
                    spinnerButton("Save new master password", saveNewMasterPassword,
                        `${compactButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
                        "button", () => !newMasterPassword.val),
                ),
            ),
        ),
    ) : "";

    const createSecretDialog = () => createSecretOverlay.val ? div(
        {class: "fixed inset-0 z-50 bg-black/60 flex items-center justify-center p-6"},
        div({class: "card w-full max-w-xl flex flex-col gap-3 border-gray-600"},
            div({class: "flex items-center justify-between gap-4"},
                h2({class: "text-base font-semibold"}, "Create secret"),
                button({
                    type: "button",
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: closeCreateSecret,
                }, "Close"),
            ),
            p({class: "text-xs text-gray-400"}, "Create a secret, then reference it from this setting."),
            labelEl({class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Secret name"),
                input({
                    class: `${inputClass} font-mono`,
                    value: createSecretName,
                    oninput: (e) => { createSecretName.val = e.target.value; },
                }),
            ),
            labelEl({class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Secret value"),
                div({class: "relative"},
                    input({
                        class: `${inputClass} pr-10 font-mono`,
                        type: () => createSecretRevealed.val ? "text" : "password",
                        value: createSecretValue,
                        oninput: (e) => { createSecretValue.val = e.target.value; },
                    }),
                    button({
                        type: "button",
                        title: () => createSecretRevealed.val ? "Hide secret" : "Reveal secret",
                        "aria-label": () => createSecretRevealed.val ? "Hide secret" : "Reveal secret",
                        disabled: () => !createSecretValue.val,
                        class: () => `absolute right-1 top-1/2 -translate-y-1/2 p-1.5 rounded text-gray-300 ` +
                            `hover:bg-gray-700 cursor-pointer ${createSecretValue.val ? "" : "invisible"}`,
                        onclick: () => { createSecretRevealed.val = !createSecretRevealed.val; },
                    }, () => createSecretRevealed.val ? eyeOffIcon() : eyeOpenIcon()),
                ),
            ),
            div({class: "flex items-center justify-end gap-2"},
                button({
                    type: "button",
                    class: `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
                    onclick: closeCreateSecret,
                }, "Cancel"),
                spinnerButton("Create secret", saveCreatedSecret,
                    `${compactButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
                    "button", () => !createSecretName.val.trim() || !createSecretValue.val),
            ),
        ),
    ) : "";

    const recoveryCard = () => {
        if (!recoveryStatus.val) {
            return div({class: "card w-full flex flex-col gap-2"},
                p({class: "text-gray-400 text-sm"}, "Loading recovery code status..."),
            );
        }
        if (!recoveryStatus.val.unlocked) {
            return div(
                {class: "card w-full flex flex-col gap-2"},
                h2({class: "text-base font-semibold"}, "Recovery code"),
                p({class: "text-xs text-amber-400"},
                    "Unlock the secrets store on the Secrets page before managing the recovery code."),
            );
        }
        if (recoveryCode.val) {
            return div(
                {class: "card w-full flex flex-col gap-2 border-amber-600"},
                h2({class: "text-base font-semibold text-amber-400"}, "Save your recovery code"),
                p({class: "text-xs text-gray-400"},
                    "This is shown only once and is not stored anywhere. Keep it somewhere safe. " +
                    "It is the only way to recover secrets if this machine is lost."),
                pre({class: "bg-gray-900 rounded-lg p-2 text-brand font-mono text-xs whitespace-pre-wrap break-all"},
                    recoveryCode.val),
                div(spinnerButton("I've saved it", async () => { recoveryCode.val = ""; },
                    `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600`)),
            );
        }
        const configured = recoveryStatus.val.recoveryConfigured;
        return div(
            {class: "card w-full flex flex-col gap-2"},
            div({class: "flex items-center justify-between gap-4"},
                div({class: "flex flex-col gap-1"},
                    h2({class: "text-base font-semibold"}, "Recovery code"),
                    configured
                        ? p({class: "text-xs text-green-400"}, "A recovery code is configured.")
                        : p({class: "text-xs text-amber-400"},
                            "No recovery code configured. Generate one so secrets can be recovered if this machine is lost."),
                ),
                spinnerButton(
                    configured ? "Regenerate recovery code" : "Generate recovery code",
                    generateRecovery,
                    `${compactButtonClass} whitespace-nowrap ${configured
                        ? "bg-gray-700 text-gray-200 hover:bg-gray-600"
                        : "bg-brand text-white hover:bg-blue-600"}`),
            ),
            div({class: "border-t border-gray-800 pt-2 mt-1 flex flex-col gap-2"},
                div({class: "flex items-center justify-between gap-3"},
                    p({class: "text-xs text-gray-400"},
                        "Use this when restoring the primary on a new machine. Replace placeholder values before running."),
                    button({
                        type: "button",
                        class: `${compactButtonClass} whitespace-nowrap bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
                        onclick: () => { recoveryExampleOpen.val = !recoveryExampleOpen.val; },
                    }, () => recoveryExampleOpen.val ? "Hide example" : "Show example"),
                ),
                () => recoveryExampleOpen.val ? pre({
                    class: "bg-gray-950 rounded-lg p-3 text-gray-200 font-mono text-xs whitespace-pre-wrap break-words overflow-x-auto",
                }, recoveryInstallExample()) : "",
            ),
        );
    };

    const compactSpaceButtonClass = "h-7 px-2.5 py-0.5 rounded-md text-xs leading-none";
    const disabledSpaceButtonClass = `${compactSpaceButtonClass} bg-gray-800 text-gray-500 cursor-not-allowed`;
    const secondarySpaceButtonClass = `${compactSpaceButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`;
    const dangerSpaceButtonClass = `${compactSpaceButtonClass} bg-red-950/60 text-red-200 hover:bg-red-900 cursor-pointer`;

    const spaceRow = (space) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-1 pr-3 align-middle"},
            () => editingSpaceID.val === space.id
                ? input({
                    class: `${inputClass} max-w-md py-0.5 text-sm`,
                    disabled: () => spaceSaving.val,
                    value: editingSpaceName,
                    oninput: (e) => { editingSpaceName.val = e.target.value; },
                    onkeydown: (e) => {
                        if (e.key === "Enter") saveRenamedSpace(space);
                        if (e.key === "Escape") resetSpaceDraft();
                    },
                })
                : div({class: "flex items-center gap-2"},
                    span({class: "text-gray-200"}, space.name || `space ${space.id}`),
                ),
        ),
        td({class: "py-1 pl-4 text-right whitespace-nowrap align-middle"},
            () => {
                if (isDefaultSpace(space)) {
                    return div({class: "flex items-center justify-end gap-2"},
                        button({type: "button", disabled: true, class: disabledSpaceButtonClass}, "Rename"),
                        button({type: "button", disabled: true, class: disabledSpaceButtonClass}, "Remove"),
                    );
                }
                if (editingSpaceID.val === space.id) {
                    return div({class: "flex items-center justify-end gap-2"},
                        spinnerButton("Save", () => saveRenamedSpace(space),
                            `${compactSpaceButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
                            "button", () => spaceSaving.val || !editingSpaceName.val.trim()),
                        button({
                            type: "button",
                            disabled: () => spaceSaving.val,
                            class: secondarySpaceButtonClass,
                            onclick: resetSpaceDraft,
                        }, "Discard"),
                    );
                }
                return div({class: "flex items-center justify-end gap-2"},
                    button({
                        type: "button",
                        disabled: () => spaceSaving.val,
                        class: secondarySpaceButtonClass,
                        onclick: () => startRenameSpace(space),
                    }, "Rename"),
                    spinnerButton("Remove", () => removeSpace(space), dangerSpaceButtonClass,
                        "button", () => spaceSaving.val),
                );
            },
        ),
    );

    const spacesCard = () => div(
        {class: "card flex flex-col gap-3"},
        div({class: "flex flex-col gap-1 pb-2 border-b border-gray-700"},
            h2({class: "text-base font-semibold"}, "Spaces"),
        ),
        () => table(
            {class: "w-full text-sm"},
            tbody(...(spacesS.val || []).map(spaceRow)),
        ),
        () => addingSpace.val ? div({class: "flex flex-col sm:flex-row sm:items-center gap-2 pt-1"},
            input({
                class: `${inputClass} max-w-md`,
                disabled: () => spaceSaving.val,
                placeholder: "New space name",
                value: newSpaceName,
                oninput: (e) => { newSpaceName.val = e.target.value; },
                onkeydown: (e) => {
                    if (e.key === "Enter") saveNewSpace();
                    if (e.key === "Escape") resetSpaceDraft();
                },
            }),
            div({class: "flex items-center gap-2"},
                spinnerButton("Save", saveNewSpace,
                    `${compactSpaceButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
                    "button", () => spaceSaving.val || !newSpaceName.val.trim()),
                button({
                    type: "button",
                    disabled: () => spaceSaving.val,
                    class: secondarySpaceButtonClass,
                    onclick: resetSpaceDraft,
                }, "Discard"),
            ),
        ) : button({
            type: "button",
            disabled: () => spaceSaving.val,
            class: `${compactSpaceButtonClass} self-start bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer`,
            onclick: () => {
                editingSpaceID.val = null;
                editingSpaceName.val = "";
                addingSpace.val = true;
                newSpaceName.val = "";
            },
        }, "Add space"),
    );

    const rowEl = (section, setting) => div(
        {class: () => isSectionSettingVisible(section, setting)
            ? "flex flex-col gap-1 border-b border-gray-800 py-1 last:border-0 sm:flex-row sm:items-center"
            : "hidden"},
        div({class: "whitespace-nowrap pr-3 sm:w-80 sm:min-w-80"},
            span({class: "text-xs text-gray-200"}, setting.label),
        ),
        div({class: "min-w-0 flex-1 text-white"}, valueInput(setting, draft, error, patchDraft, saving, secrets, openCreateSecret)),
        div({class: "w-20 whitespace-nowrap text-right sm:pl-4"},
            () => {
                const item = draft.val?.[setting.key];
                return span({class: () => `inline-block w-16 text-xs ${item && isDirty(setting, item) ? "text-blue-300" : "invisible"}`}, "changed");
            },
        ),
    );

    const sectionHeader = (title, right = "") => div(
        {class: "flex items-center justify-between gap-3 pb-2 border-b border-gray-700"},
        h2({class: "text-sm font-semibold text-blue-300"}, title),
        right,
    );

    const isSectionSettingVisible = (section, setting) => {
        if (setting.visible && !setting.visible(draft.val)) return false;
        if (section.title === "Web UI" && ["ACME_HOSTS", "ACME_EMAIL"].includes(setting.key)) {
            return !effectiveDraftBoolValue(draft.val?.WEB_TLS_SELF_MANAGED);
        }
        return !section.enabledKey
            || setting.key === section.enabledKey
            || effectiveDraftBoolValue(draft.val?.[section.enabledKey]);
    };

    const settingsItems = (section) => div(
        {class: "flex flex-col text-xs"},
        ...section.settings.map(setting => rowEl(section, setting)),
    );

    const settingsSection = (section, index) => div(
        {class: "flex flex-col gap-2"},
        sectionHeader(section.title, index === 0 ? dirtyActions : ""),
        settingsItems(section),
    );

    const settingsCard = () => div(
        {class: "card flex flex-col gap-4"},
        ...settingsSections.map(settingsSection),
    );

    const dirtyActions = () => dirtyCount.val ? div({class: "flex items-center gap-2"},
        span({class: "text-sm font-medium text-amber-300"}, "Unsaved changes"),
        button({
            type: "button",
            disabled: () => saving.val,
            class: `${compactButtonClass} bg-gray-700 text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer whitespace-nowrap`,
            onclick: resetChanges,
        }, "Reset changes"),
        spinnerButton("Save changes", saveChanges,
            `${compactButtonClass} bg-brand text-white hover:bg-blue-600 whitespace-nowrap`,
            "button", () => saving.val),
    ) : "";

    settingsPageNode = div(
        {class: "settings-scroll h-full min-h-0 overflow-y-auto overflow-x-hidden p-3 flex flex-col gap-3"},
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : "",
        p({class: () => loaded.val ? "hidden" : "text-gray-400"}, "Loading..."),
        div(
            {class: () => loaded.val ? "flex flex-col gap-3" : "hidden"},
            settingsCard(),
        ),
        masterPasswordCard,
        recoveryCard,
        spacesCard,
        masterPasswordVerifyDialog,
        masterPasswordUpdateOverlay,
        createSecretDialog,
    );
    return settingsPageNode;
}
