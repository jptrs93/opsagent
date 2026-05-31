import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";

const { div, h1, h2, p, span, input, table, thead, tbody, tr, th, td, code, pre } = van.tags;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime()) || d.getTime() === 0) return '-';
    return d.toLocaleString();
};

export function secretsPage() {
    const status = van.state(null);   // {unlocked, recoveryConfigured} | null
    const secrets = van.state(null);  // [SecretMeta] | null
    const error = van.state(null);
    const recoveryCode = van.state(null); // shown once after generation

    const loadStatus = async () => {
        status.val = await capi.postV1SecretsStatus({});
    };
    const loadSecrets = async () => {
        if (!status.val || !status.val.unlocked) { secrets.val = []; return; }
        const res = await capi.postV1SecretsList({});
        secrets.val = res.items || [];
    };
    const reload = async () => {
        try {
            error.val = null;
            await loadStatus();
            await loadSecrets();
        } catch (e) {
            error.val = e.message;
        }
    };

    reload();

    // --- add/update form state ---
    const fName = van.state("");
    const fGroup = van.state("");
    const fValue = van.state("");

    const saveSecret = async () => {
        if (!fName.val.trim()) { error.val = "Secret name is required"; return; }
        try {
            error.val = null;
            await capi.postV1SecretsSet({
                name: fName.val.trim(),
                group: fGroup.val.trim(),
                value: new TextEncoder().encode(fValue.val),
            });
            fName.val = ""; fGroup.val = ""; fValue.val = "";
            await loadSecrets();
        } catch (e) {
            error.val = e.message;
        }
    };

    const deleteSecret = async (name) => {
        try {
            error.val = null;
            await capi.postV1SecretsDelete({name});
            delete revealed.val[name];
            revealed.val = {...revealed.val};
            await loadSecrets();
        } catch (e) {
            error.val = e.message;
        }
    };

    // name -> decoded plaintext, for secrets the operator has revealed.
    const revealed = van.state({});
    const revealSecret = async (name) => {
        try {
            error.val = null;
            const res = await capi.postV1SecretsReveal({name});
            revealed.val = {...revealed.val, [name]: new TextDecoder().decode(res.value)};
        } catch (e) {
            error.val = e.message;
        }
    };
    const hideSecret = (name) => {
        delete revealed.val[name];
        revealed.val = {...revealed.val};
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

    const generateRecovery = async () => {
        try {
            error.val = null;
            const res = await capi.postV1SecretsGenerateRecoveryCode({});
            recoveryCode.val = res.code;
            await loadStatus();
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

    const recoveryCard = () => {
        if (recoveryCode.val) {
            return div(
                {class: "card flex flex-col gap-3 max-w-xl border-amber-600"},
                h2({class: "text-base font-semibold text-amber-400"}, "Save your recovery code"),
                p({class: "text-sm text-gray-400"},
                    "This is shown only once and is not stored anywhere. Keep it somewhere safe — " +
                    "it is the only way to recover secrets if this machine is lost."),
                pre({class: "bg-gray-900 rounded-lg p-3 text-brand font-mono text-sm whitespace-pre-wrap break-all"},
                    recoveryCode.val),
                div(spinnerButton("I've saved it", async () => { recoveryCode.val = null; }, "btn-secondary")),
            );
        }
        const configured = status.val && status.val.recoveryConfigured;
        return div(
            {class: "card flex flex-col gap-3 max-w-xl"},
            h2({class: "text-base font-semibold"}, "Recovery code"),
            configured
                ? p({class: "text-sm text-green-400"}, "A recovery code is configured.")
                : p({class: "text-sm text-amber-400"},
                    "No recovery code configured. Generate one so secrets can be recovered if this machine is lost."),
            div(spinnerButton(
                configured ? "Regenerate recovery code" : "Generate recovery code",
                generateRecovery,
                configured ? "btn-secondary" : "btn-primary")),
        );
    };

    const addForm = () => div(
        {class: "card flex flex-col gap-3 max-w-xl"},
        h2({class: "text-base font-semibold"}, "Add or update a secret"),
        p({class: "text-sm text-gray-400"},
            "Reference a secret from a deployment's env value as ",
            code({class: "font-mono text-gray-300"}, "${name}"), "."),
        div({class: "flex flex-col gap-1"},
            span({class: "text-xs text-gray-400"}, "Name"),
            input({
                class: "text-input font-mono", type: "text",
                placeholder: "staging.db.password",
                value: fName, oninput: (e) => fName.val = e.target.value,
            })),
        div({class: "flex flex-col gap-1"},
            span({class: "text-xs text-gray-400"}, "Group (optional)"),
            input({
                class: "text-input", type: "text",
                placeholder: "staging",
                value: fGroup, oninput: (e) => fGroup.val = e.target.value,
            })),
        div({class: "flex flex-col gap-1"},
            span({class: "text-xs text-gray-400"}, "Value"),
            input({
                class: "text-input font-mono", type: "password",
                placeholder: "secret value",
                value: fValue, oninput: (e) => fValue.val = e.target.value,
            })),
        div(spinnerButton("Save secret", saveSecret, "btn-primary", "button",
            () => !fName.val.trim())),
    );

    const secretsTable = () => {
        if (secrets.val === null) return p({class: "text-gray-400"}, "Loading...");
        if (secrets.val.length === 0) return p({class: "text-gray-400"}, "No secrets yet.");
        return div(
            {class: "card"},
            table(
                {class: "w-full text-sm"},
                thead(
                    tr({class: "text-left text-gray-400 border-b border-gray-700"},
                        th({class: "pb-2 pr-6"}, "Name"),
                        th({class: "pb-2 pr-6"}, "Group"),
                        th({class: "pb-2 pr-6"}, "Value"),
                        th({class: "pb-2 pr-6"}, "Updated"),
                        th({class: "pb-2"}, ""),
                    )
                ),
                tbody(
                    ...secrets.val.map(s =>
                        tr({class: "border-b border-gray-800 last:border-0"},
                            td({class: "py-3 pr-6 text-white font-mono"}, s.name),
                            td({class: "py-3 pr-6 text-gray-300"}, s.group || '-'),
                            td({class: "py-3 pr-6"}, () => s.name in revealed.val
                                ? span({class: "flex items-center gap-2"},
                                    code({class: "font-mono text-brand break-all"}, revealed.val[s.name] || '(empty)'),
                                    spinnerButton("Hide", () => hideSecret(s.name), "btn-secondary", "button"))
                                : spinnerButton("Reveal", () => revealSecret(s.name), "btn-secondary", "button")),
                            td({class: "py-3 pr-6 text-gray-400"}, formatTime(s.updatedAt)),
                            td({class: "py-3 text-right"},
                                spinnerButton("Delete", () => deleteSecret(s.name),
                                    "btn-secondary text-red-300", "button")),
                        )
                    )
                )
            )
        );
    };

    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Secrets"),
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : div(),
        () => {
            if (status.val === null) return p({class: "text-gray-400"}, "Loading...");
            if (!status.val.unlocked) return lockedSection();
            return div(
                {class: "flex flex-col gap-6"},
                addForm(),
                secretsTable(),
                recoveryCard(),
            );
        },
    );
}
