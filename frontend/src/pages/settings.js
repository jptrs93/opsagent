import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";

const { div, h1, p, span, table, thead, tbody, tr, th, td, button, code, input, select, option } = van.tags;

const boolValue = (value) => value ? "true" : "false";
const listValue = (value) => value && value.length ? value.join(", ") : "";

const configRows = (cfg) => [
    textRow("Web UI server listen", "WEB_LISTEN", cfg.webListen, true),
    boolRow("Web UI disable HTTPS", "WEB_HTTP_ONLY", cfg.webHttpOnly, true),
    textRow("Cluster listen", "CLUSTER_LISTEN", cfg.clusterListen, true),
    textRow("Enrollment listen", "ENROLLMENT_LISTEN", cfg.enrollmentListen, true),
    textRow("Web UI ACME hosts", "ACME_HOSTS", listValue(cfg.acmeHosts), true),
    textRow("Web UI email", "ACME_EMAIL", cfg.acmeEmail, true),
    secretRow("GitHub token", "GITHUB_TOKEN", cfg.githubToken),
    textRow("Backup S3 access key ID", "BACKUP_S3_ACCESS_KEY_ID", cfg.backupS3AccessKeyId),
    secretRow("Backup S3 secret access key", "BACKUP_S3_SECRET_ACCESS_KEY", cfg.backupS3SecretAccessKey),
    textRow("Backup S3 bucket", "BACKUP_S3_BUCKET", cfg.backupS3Bucket),
    textRow("Backup S3 path", "BACKUP_S3_PATH", cfg.backupS3Path),
    textRow("Backup S3 region", "BACKUP_S3_REGION", cfg.backupS3Region),
    textRow("Backup S3 endpoint", "BACKUP_S3_ENDPOINT", cfg.backupS3Endpoint),
];

function textRow(label, key, value, restartRequired = false) {
    const original = value || "";
    return {
        label,
        key,
        type: "text",
        restartRequired,
        value: van.state(original),
        original,
    };
}

function boolRow(label, key, value, restartRequired = false) {
    const original = boolValue(value);
    return {
        label,
        key,
        type: "bool",
        restartRequired,
        value: van.state(original),
        original,
    };
}

function secretRow(label, key, secret) {
    return {
        label,
        key,
        type: "secret",
        secret,
        value: van.state(""),
        revealed: van.state(null),
        cleared: van.state(false),
    };
}

const isRowDirty = (row) => {
    if (row.type === "secret") {
        return row.value.val !== "" || row.cleared.val;
    }
    return row.value.val !== row.original;
};

const revealSecret = async (row, error) => {
    if (!row.secret?.key) return;
    if (row.revealed.val !== null) { row.revealed.val = null; return; }
    try {
        error.val = null;
        const res = await capi.postV1SecretValueReveal({key: row.secret.key});
        row.revealed.val = new TextDecoder().decode(res.value);
    } catch (e) {
        error.val = e.message;
    }
};

const inputClass = "w-full min-w-64 bg-transparent px-2 py-1 rounded border border-gray-700 " +
    "hover:border-gray-600 focus:border-brand focus:outline-none";

function valueInput(row, error) {
    if (row.type === "bool") {
        return select({
            class: inputClass,
            value: row.value,
            onchange: (e) => row.value.val = e.target.value,
        },
            option({value: "true"}, "true"),
            option({value: "false"}, "false"),
        );
    }
    if (row.type === "secret") {
        return div(
            {class: "flex flex-col gap-2"},
            div({class: "flex flex-wrap items-center gap-2"},
                row.secret?.key
                    ? code({class: "text-xs text-blue-300 bg-gray-900 px-2 py-1 rounded"}, row.secret.key)
                    : span({class: "text-gray-500 text-sm"}, "not configured"),
                () => row.cleared.val
                    ? span({class: "text-xs text-amber-300"}, "will be cleared")
                    : null,
                row.secret?.key ? button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer",
                    onclick: () => revealSecret(row, error),
                }, () => row.revealed.val === null ? "Reveal" : "Hide") : null,
                row.secret?.key ? button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium text-gray-200 bg-gray-700 hover:bg-gray-600 cursor-pointer",
                    onclick: () => { row.value.val = ""; row.cleared.val = true; },
                }, "Clear") : null,
            ),
            () => row.revealed.val === null ? null : code({
                class: "text-xs text-amber-200 bg-amber-950/40 px-2 py-1 rounded break-all",
            }, row.revealed.val),
            input({
                class: `${inputClass} font-mono`,
                type: "password",
                placeholder: row.secret?.key ? "new value leaves existing secret unchanged" : "new value",
                value: row.value,
                oninput: (e) => { row.value.val = e.target.value; row.cleared.val = false; },
            }),
        );
    }
    return input({
        class: inputClass,
        value: row.value,
        oninput: (e) => row.value.val = e.target.value,
    });
}

export function settingsPage() {
    const config = van.state(null);
    const rows = van.state(null);
    const error = van.state(null);

    const load = async () => {
        try {
            error.val = null;
            config.val = await capi.getV1Config();
            rows.val = configRows(config.val);
        } catch (e) {
            error.val = e.message;
        }
    };
    load();

    const dirtyRows = () => (rows.val || []).filter(isRowDirty);

    const saveChanges = async () => {
        try {
            error.val = null;
            const res = await capi.postV1ConfigUpdate({
                values: dirtyRows().map((row) => ({key: row.key, value: row.value.val})),
            });
            config.val = res;
            rows.val = configRows(res);
        } catch (e) {
            error.val = e.message;
        }
    };

    const rowEl = (row) => tr(
        {class: "border-b border-gray-800 last:border-0 align-top"},
        td({class: "py-3 pr-6 whitespace-nowrap"},
            span({class: "text-gray-200"}, row.label),
            row.restartRequired ? span({class: "ml-2 text-xs text-amber-300"}, "restart required") : null,
        ),
        td({class: "py-2 text-white"}, valueInput(row, error)),
        td({class: "py-3 pl-4 text-right w-px whitespace-nowrap"},
            () => isRowDirty(row) ? span({class: "text-xs text-blue-300"}, "changed") : null,
        ),
    );

    return div(
        {class: "settings-scroll h-full min-h-0 overflow-y-scroll p-6 flex flex-col gap-6"},
        div({class: "flex items-start justify-between gap-4"},
            div(
                h1({class: "text-xl font-bold"}, "Configuration"),
                p({class: "text-sm text-gray-400 mt-1"},
                    "Edit runtime configuration. Changes are staged locally and applied together."),
            ),
            () => dirtyRows().length
                ? spinnerButton("Save changes", saveChanges, "btn-primary whitespace-nowrap")
                : null,
        ),
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : null,
        () => {
            if (rows.val === null) return p({class: "text-gray-400"}, "Loading...");
            return div(
                {class: "card overflow-hidden"},
                div(
                    {class: "overflow-x-auto"},
                    table(
                        {class: "w-full text-sm"},
                        thead(
                            tr({class: "text-left text-gray-400 border-b border-gray-700"},
                                th({class: "pb-2 pr-6 font-medium"}, "Setting"),
                                th({class: "pb-2 font-medium"}, "Value"),
                                th({class: "pb-2 w-px"}, ""),
                            )
                        ),
                        tbody(...rows.val.map(rowEl)),
                    ),
                ),
            );
        },
    );
}
