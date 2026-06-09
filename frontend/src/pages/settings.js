import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { div, h1, h2, p, span, table, thead, tbody, tr, th, td, button, code } = van.tags;

const textValue = (value) => value === undefined || value === null || value === "" ? "-" : String(value);
const boolValue = (value) => value ? "true" : "false";
const listValue = (value) => value && value.length ? value.join(", ") : "-";

const revealSecret = async (secret, revealed, error) => {
    if (!secret?.key) return;
    if (revealed.val !== null) { revealed.val = null; return; }
    try {
        error.val = null;
        const res = await capi.postV1SecretValueReveal({key: secret.key});
        revealed.val = new TextDecoder().decode(res.value);
    } catch (e) {
        error.val = e.message;
    }
};

function configTable(rows) {
    return div(
        {class: "card overflow-hidden"},
        table(
            {class: "w-full text-sm"},
            thead(
                tr({class: "text-left text-gray-400 border-b border-gray-700"},
                    th({class: "pb-2 pr-6"}, "Setting"),
                    th({class: "pb-2"}, "Value"),
                )
            ),
            tbody(
                ...rows.map(([label, value]) => tr(
                    {class: "border-b border-gray-800 last:border-0"},
                    td({class: "py-3 pr-6 text-gray-400 whitespace-nowrap"}, label),
                    td({class: "py-3 text-white break-all"}, value),
                ))
            )
        )
    );
}

function secretValue(label, secret, error) {
    const revealed = van.state(null);
    if (!secret?.key) {
        return [label, span({class: "text-gray-500"}, "not configured")];
    }
    return [label, div(
        {class: "flex flex-wrap items-center gap-3"},
        code({class: "text-xs text-blue-300 bg-surface px-2 py-1 rounded"}, secret.key),
        () => revealed.val === null
            ? span({class: "text-gray-500"}, "hidden")
            : code({class: "text-xs text-amber-200 bg-amber-950/40 px-2 py-1 rounded break-all"}, revealed.val),
        button({
            type: "button",
            class: "text-xs px-3 py-1 rounded-md font-medium text-gray-200 bg-surface hover:bg-surface-hover cursor-pointer",
            onclick: () => revealSecret(secret, revealed, error),
        }, () => revealed.val === null ? "Reveal" : "Hide"),
    )];
}

export function settingsPage() {
    const config = van.state(null);
    const error = van.state(null);

    const load = async () => {
        try {
            error.val = null;
            config.val = await capi.getV1Config();
        } catch (e) {
            error.val = e.message;
        }
    };
    load();

    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        div(
            h1({class: "text-xl font-bold"}, "Configuration"),
            p({class: "text-sm text-gray-400 mt-1"}, "Read-only runtime configuration. Secret values are revealed only on request."),
        ),
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : null,
        () => {
            const cfg = config.val;
            if (!cfg) return p({class: "text-gray-400"}, "Loading...");
            return div(
                {class: "flex flex-col gap-6"},
                div(
                    {class: "flex flex-col gap-3"},
                    h2({class: "text-base font-semibold"}, "Web"),
                    configTable([
                        ["Web listen", textValue(cfg.webListen)],
                        ["HTTP only", boolValue(cfg.webHttpOnly)],
                        ["ACME hosts", listValue(cfg.acmeHosts)],
                        ["ACME email", textValue(cfg.acmeEmail)],
                    ]),
                ),
                div(
                    {class: "flex flex-col gap-3"},
                    h2({class: "text-base font-semibold"}, "Cluster"),
                    configTable([
                        ["Cluster listen", textValue(cfg.clusterListen)],
                        ["Enrollment listen", textValue(cfg.enrollmentListen)],
                    ]),
                ),
                div(
                    {class: "flex flex-col gap-3"},
                    h2({class: "text-base font-semibold"}, "Repository Credentials"),
                    configTable([
                        secretValue("GitHub token", cfg.githubToken, error),
                    ]),
                ),
                div(
                    {class: "flex flex-col gap-3"},
                    h2({class: "text-base font-semibold"}, "Backup"),
                    configTable([
                        ["S3 access key ID", textValue(cfg.backupS3AccessKeyId)],
                        secretValue("S3 secret access key", cfg.backupS3SecretAccessKey, error),
                        ["S3 bucket", textValue(cfg.backupS3Bucket)],
                        ["S3 path", textValue(cfg.backupS3Path)],
                        ["S3 region", textValue(cfg.backupS3Region)],
                        ["S3 endpoint", textValue(cfg.backupS3Endpoint)],
                    ]),
                ),
            );
        },
    );
}
