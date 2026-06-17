import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";

const { div, h2, p, pre, span, table, tbody, tr, td, button, code, input, select, option, label: labelEl } = van.tags;
const { svg, path, circle, line } = van.tags("http://www.w3.org/2000/svg");

const boolValue = (value) => value ? "true" : "false";
const listValue = (value) => value && value.length ? value.join(", ") : "";

const settings = [
    {label: "Web UI server listen", key: "WEB_LISTEN", type: "text", value: (cfg) => cfg.webListen},
    {label: "Web UI disable HTTPS", key: "WEB_HTTP_ONLY", type: "bool", value: (cfg) => boolValue(cfg.webHttpOnly)},
    {label: "Web UI ACME hosts", key: "ACME_HOSTS", type: "text", value: (cfg) => listValue(cfg.acmeHosts)},
    {label: "Web UI email", key: "ACME_EMAIL", type: "text", value: (cfg) => cfg.acmeEmail},
    {label: "Cluster listen", key: "CLUSTER_LISTEN", type: "text", value: (cfg) => cfg.clusterListen},
    {label: "Cluster enrollment listen", key: "ENROLLMENT_LISTEN", type: "text", value: (cfg) => cfg.enrollmentListen},
    {label: "GitHub token", key: "GITHUB_TOKEN", type: "secret", secret: (cfg) => cfg.githubToken, defaultSecretName: "opendeploy.config.github_token"},
    {label: "Backup enabled", key: "BACKUP_ENABLED", type: "bool", value: (cfg) => boolValue(cfg.backupEnabled)},
    {label: "Backup S3 access key ID", key: "BACKUP_S3_ACCESS_KEY_ID", type: "text", value: (cfg) => cfg.backupS3AccessKeyId},
    {label: "Backup S3 secret access key", key: "BACKUP_S3_SECRET_ACCESS_KEY", type: "secret", secret: (cfg) => cfg.backupS3SecretAccessKey, defaultSecretName: "opendeploy.config.backup_s3_secret_access_key"},
    {label: "Backup S3 bucket", key: "BACKUP_S3_BUCKET", type: "text", value: (cfg) => cfg.backupS3Bucket},
    {label: "Backup S3 path", key: "BACKUP_S3_PATH", type: "text", value: (cfg) => cfg.backupS3Path},
    {label: "Backup S3 region", key: "BACKUP_S3_REGION", type: "text", value: (cfg) => cfg.backupS3Region},
    {label: "Backup S3 endpoint", key: "BACKUP_S3_ENDPOINT", type: "text", value: (cfg) => cfg.backupS3Endpoint},
];

const draftValue = (setting, cfg) => {
    if (setting.type === "secret") {
        const secret = setting.secret(cfg);
        return {
            value: secret?.key || "",
            original: secret?.key || "",
            cleared: false,
            revealed: false,
            revealedValue: "",
        };
    }
    const original = setting.value(cfg) || "";
    return {value: original, original};
};

const configDraft = (cfg) => Object.fromEntries(settings.map((setting) => [setting.key, draftValue(setting, cfg)]));

const isDirty = (setting, item) => setting.type === "secret"
    ? item.value !== item.original
    : item.value !== item.original;

const dirtySettingsFor = (draft) => settings
    .map((setting) => ({setting, item: draft?.[setting.key]}))
    .filter(({setting, item}) => item && isDirty(setting, item));

const inputClass = "w-full min-w-64 bg-transparent px-2 py-1 rounded border border-gray-700 " +
    "hover:border-gray-600 focus:border-brand focus:outline-none";
const compactButtonClass = "h-8 px-3 py-1 rounded-md text-sm leading-none";

const iconBase = {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "2",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    class: "w-4 h-4",
};

const eyeIcon = () => svg(iconBase,
    path({d: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"}),
    circle({cx: "12", cy: "12", r: "3"}),
);

const eyeOffIcon = () => svg(iconBase,
    path({d: "M9.9 4.24A9.1 9.1 0 0 1 12 4c7 0 10 8 10 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"}),
    path({d: "M6.61 6.61A18.5 18.5 0 0 0 2 12s3 8 10 8a9.1 9.1 0 0 0 5.39-1.61"}),
    line({x1: "2", y1: "2", x2: "22", y2: "22"}),
);

const copyIcon = () => svg(iconBase,
    path({d: "M8 8h11a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z"}),
    path({d: "M4 16c-.55 0-1-.45-1-1V4c0-.55.45-1 1-1h11c.55 0 1 .45 1 1"}),
);

function valueInput(setting, draft, error, patchDraft, saving, secrets, openCreateSecret) {
    const item = () => draft.val?.[setting.key];
    const patch = (next) => patchDraft(setting.key, next);

    if (setting.type === "bool") {
        return labelEl(
            {class: "inline-flex items-center gap-3 cursor-pointer select-none"},
            input({
                class: "sr-only peer",
                type: "checkbox",
                disabled: () => saving.val,
                checked: () => item()?.value === "true",
                onchange: (e) => patch({value: e.target.checked ? "true" : "false"}),
            }),
            span({
                class: () => `relative h-6 w-11 rounded-full transition-colors ${
                    item()?.value === "true" ? "bg-brand" : "bg-gray-700"
                } before:absolute before:left-1 before:top-1 before:h-4 before:w-4 before:rounded-full ` +
                    `before:bg-white before:transition-transform ${item()?.value === "true" ? "before:translate-x-5" : ""}`,
            }),
            span({class: "text-sm text-gray-300"}, () => item()?.value === "true" ? "true" : "false"),
        );
    }
    if (setting.type === "secret") {
        const revealSecret = async () => {
            const current = item();
            if (!current?.value) return;
            if (current.revealed) { patch({revealed: false, revealedValue: ""}); return; }
            try {
                error.val = null;
                const res = await capi.postV1SecretValueReveal({key: current.value});
                patch({revealed: true, revealedValue: new TextDecoder().decode(res.value)});
            } catch (e) {
                error.val = e.message;
            }
        };
        const options = () => {
            const current = item()?.value || "";
            const list = secrets.val || [];
            if (!current || list.some(s => s.name === current)) return list;
            return [{name: current}, ...list];
        };

        return div(
            {class: "flex items-center gap-2"},
            () => select({
                class: inputClass,
                disabled: () => saving.val,
                value: () => item()?.value || "",
                onchange: (e) => patch({value: e.target.value, revealed: false, revealedValue: ""}),
            },
                option({value: "", selected: () => !item()?.value}, "No secret selected"),
                ...options().map(s => option({value: s.name, selected: s.name === item()?.value}, s.name)),
            ),
            button({
                type: "button",
                disabled: () => saving.val,
                class: "text-xs px-3 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer whitespace-nowrap",
                onclick: () => openCreateSecret(setting),
            }, "Create secret"),
            () => item()?.value ? button({
                type: "button",
                title: () => item().revealed ? "Hide saved secret" : "Reveal saved secret",
                "aria-label": () => item().revealed ? "Hide saved secret" : "Reveal saved secret",
                disabled: () => saving.val,
                class: "p-1.5 rounded text-gray-300 bg-gray-700 hover:bg-gray-600 cursor-pointer",
                onclick: revealSecret,
            }, () => item().revealed ? eyeOffIcon() : eyeIcon()) : "",
            () => item()?.value ? button({
                type: "button",
                disabled: () => saving.val,
                class: "text-xs px-3 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer whitespace-nowrap",
                onclick: () => patch({value: "", revealed: false, revealedValue: ""}),
            }, "Clear") : "",
            () => item()?.revealed ? code({
                class: "text-xs text-amber-200 bg-amber-950/40 px-2 py-1 rounded truncate max-w-64",
                title: item().revealedValue,
            }, item().revealedValue) : "",
        );
    }
    return input({
        class: inputClass,
        disabled: () => saving.val,
        value: () => item()?.value || "",
        oninput: (e) => patch({value: e.target.value}),
    });
}

export function settingsPage() {
    const config = van.state(null);
    const draft = van.state(null);
    const dirtyCount = van.state(0);
    const loaded = van.state(false);
    const error = van.state(null);
    const saving = van.state(false);
    const secrets = van.state([]);
    const recoveryStatus = van.state(null);
    const recoveryCode = van.state("");
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
                capi.getV1Config(),
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
    load();

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
            await capi.postV1SecretsSet({
                name,
                group: "config",
                value: new TextEncoder().encode(createSecretValue.val),
            });
            await reloadSecrets();
            patchDraft(createSecretSettingKey.val, {value: name, revealed: false, revealedValue: ""});
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
            const res = await capi.postV1ConfigUpdate({
                values: dirtySettings().map(({setting, item}) => ({key: setting.key, value: item.value})),
            });
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
                }, () => newMasterPasswordRevealed.val ? eyeOffIcon() : eyeIcon()),
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
                    }, () => createSecretRevealed.val ? eyeOffIcon() : eyeIcon()),
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
        );
    };

    const rowEl = (setting) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle"},
        td({class: "py-2 pr-3 whitespace-nowrap align-middle"},
            span({class: "text-gray-200"}, setting.label),
        ),
        td({class: "py-2 text-white"}, valueInput(setting, draft, error, patchDraft, saving, secrets, openCreateSecret)),
        td({class: "py-2 pl-4 text-right w-20 whitespace-nowrap align-middle"},
            () => {
                const item = draft.val?.[setting.key];
                return span({class: () => `inline-block w-16 text-xs ${item && isDirty(setting, item) ? "text-blue-300" : "invisible"}`}, "changed");
            },
        ),
    );

    return div(
        {class: "settings-scroll h-full min-h-0 overflow-y-auto p-3 flex flex-col gap-3"},
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : "",
        () => {
            if (!loaded.val) return p({class: "text-gray-400"}, "Loading...");
            return div(
                {class: "card flex flex-col gap-3"},
                div({class: "flex items-center justify-between gap-3 pb-2 border-b border-gray-700"},
                    h2({class: "text-base font-semibold"}, "General settings"),
                    () => dirtyCount.val ? div({class: "flex items-center gap-2"},
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
                    ) : "",
                ),
                div(
                    {class: "settings-scroll overflow-x-auto"},
                    table(
                        {class: "w-full text-sm"},
                        tbody(...settings.map(rowEl)),
                    ),
                ),
            );
        },
        masterPasswordCard,
        recoveryCard,
        masterPasswordVerifyDialog,
        masterPasswordUpdateOverlay,
        createSecretDialog,
    );
}
