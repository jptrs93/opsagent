import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {backupStatusS, deploymentsS, deploymentsStreamS, enrollmentsS, machinesS, primaryConfigS, userConfigsS} from "../state/deployments.js";

const { button, code, div, h2, input, p, span, table, tbody, td, th, thead, tr } = van.tags;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '-';
    return d.toLocaleString();
};

export function clusterPage() {
    const config = primaryConfigS;
    const enrollmentInfo = van.state(null);
    const configError = van.state(null);
    const copied = van.state(false);

    const loadEnrollmentInfo = async () => {
        try {
            configError.val = null;
            enrollmentInfo.val = await capi.getV1EnrollmentInfo();
        } catch (e) {
            configError.val = e.message || "Failed to load cluster config";
        }
    };
    loadEnrollmentInfo();

    const copyInstallCommand = async () => {
        const command = secondaryInstallCommand(config.val?.config?.settings, enrollmentInfo.val, primaryOpenDeployVersion());
        if (!command) return;
        await navigator.clipboard.writeText(command);
        copied.val = true;
        setTimeout(() => copied.val = false, 2000);
    };

    return div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto p-3 flex flex-col gap-3"},
        () => {
            if (deploymentsStreamS.val.status !== "connected" && machinesS.val.length === 0) {
                return p({class: "text-gray-400"}, "Loading...");
            }

            const sorted = [...machinesS.val].sort((a, b) => {
                if (a.isPrimary && !b.isPrimary) return -1;
                if (!a.isPrimary && b.isPrimary) return 1;
                return a.name.localeCompare(b.name);
            });
            const pending = enrollmentsS.val.filter(e => e.status === "waiting");

            return div(
                {class: "flex flex-col gap-3"},
                div(
                    {class: "card"},
                    h2({class: "font-semibold mb-4"}, "Connected nodes"),
                    sorted.length === 0
                        ? p({class: "text-gray-400 text-sm"}, "No nodes found.")
                        : table(
                            {class: "w-full text-sm"},
                            thead(
                                tr({class: "text-left text-gray-400 border-b border-gray-700"},
                                    th({class: "pb-2 pr-3 w-[24rem]"}, "Node"),
                                    th({class: "pb-2 pr-3"}, "Role"),
                                    th({class: "pb-2 pr-3"}, "Status"),
                                    th({class: "pb-2"}, "Connected since"),
                                )
                            ),
                            tbody(
                                ...sorted.map(machineRow)
                            )
                        )
                ),
                backupReplicationCard(backupStatusS),
                div(
                    {class: "card"},
                    div(
                        {class: "mb-4"},
                        h2({class: "font-semibold mb-2"}, "Enrollment requests"),
                        p({class: "text-sm text-gray-400"}, "Accept a waiting worker to issue its cluster client certificate."),
                    ),
                    pending.length === 0
                        ? p({class: "text-gray-400 text-sm"}, "No pending enrollment requests.")
                        : table(
                            {class: "w-full text-sm"},
                            thead(
                                tr({class: "text-left text-gray-400 border-b border-gray-700"},
                                    th({class: "pb-2 pr-6"}, "Request"),
                                    th({class: "pb-2 pr-6"}, "Remote IP"),
                                    th({class: "pb-2 pr-6"}, "Updated"),
                                    th({class: "pb-2"}, "Accept as"),
                                )
                            ),
                            tbody(...pending.map(req => enrollmentRow(req)))
                        ),
                    secondaryInstallPanel(config, enrollmentInfo, configError, copied, copyInstallCommand),
                )
            );
        }
    );
}

function machineRow(machine) {
    const originalName = van.state(machine.name || '');
    const name = van.state(machine.name || '');
    const saving = van.state(false);
    const error = van.state('');

    const rename = async () => {
        const nextName = name.val.trim();
        if (!nextName || saving.val) return;
        if (nextName === originalName.val) {
            name.val = originalName.val;
            return;
        }
        saving.val = true;
        error.val = '';
        try {
            await capi.postV1ClusterRename({identifier: machine.identifier, name: nextName});
            originalName.val = nextName;
            name.val = nextName;
        } catch (e) {
            error.val = e.message || 'Failed to rename node';
        } finally {
            saving.val = false;
        }
    };

    return tr(
        {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `machine-row-${machine.identifier}`},
        td({class: "py-1 pr-3 text-white font-medium w-[24rem]"},
            inlineEditableInput({
                value: name,
                dirty: () => name.val !== originalName.val,
                valid: () => Boolean(name.val.trim()),
                disabled: saving,
                oninput: event => { name.val = event.target.value; },
                onSave: rename,
                onDiscard: () => { name.val = originalName.val; },
                inputClass: "input w-full min-w-36",
                ariaLabel: `Node name for ${machine.identifier}`,
                saveAriaLabel: `Save node name ${machine.identifier}`,
                discardAriaLabel: `Discard node name change for ${machine.identifier}`,
            }),
            () => error.val ? p({class: "mt-1 text-xs text-red-400"}, error.val) : '',
        ),
        td({class: "py-1 pr-3"},
            machine.isPrimary
                ? span({class: "text-blue-400"}, "primary")
                : span({class: "text-gray-300"}, "secondary")
        ),
        td({class: "py-1 pr-3"},
            machine.connected
                ? span({class: "text-green-400"}, "connected")
                : span({class: "text-red-400"}, "disconnected")
        ),
        td({class: "py-1 text-gray-400"},
            machine.isPrimary ? '-' : formatTime(machine.connectedAt)
        ),
    );
}

function backupReplicationCard(statusS) {
    return div(
        {class: "card", "data-testid": "backup-replication-card"},
        div(
            {class: "flex items-start justify-between gap-3 mb-3"},
            div(
                h2({class: "font-semibold mb-1"}, "Backup replication"),
                p({class: "text-sm text-gray-400"}, "Primary database and large asset backup state."),
            ),
            () => backupStatusBadge(statusS.val),
        ),
        () => backupStatusDetails(statusS.val),
    );
}

function backupStatusBadge(status) {
    const label = backupStatusLabel(status);
    const klass = status?.error || status?.assetError
        ? "bg-red-950 text-red-300 border-red-800"
        : status?.inSync
            ? "bg-green-950 text-green-300 border-green-800"
            : status?.configured
                ? "bg-yellow-950 text-yellow-300 border-yellow-800"
                : "bg-gray-800 text-gray-300 border-gray-700";
    return span({class: klass + " px-2 py-1 rounded border text-xs font-medium", "data-testid": "backup-replication-status"}, label);
}

function backupStatusLabel(status) {
    if (status?.error || status?.assetError) return "error";
    if (status?.assetMigrationRunning) return status.assetTargetS3 ? "moving assets to S3" : "moving assets local";
    if (!status || !status.configured) return "not configured";
    if (!status.running) return "not running";
    if (status.inSync) return "in sync";
    return "syncing";
}

function backupStatusDetails(status) {
    if ((!status || !status.configured) && !status?.assetMigrationRunning && !status?.assetError) {
        return p({class: "text-sm text-gray-400"}, "Backups are not configured.");
    }
    return div(
        {class: "grid grid-cols-1 md:grid-cols-3 gap-3 text-sm"},
        detailCell("Local TXID", String(status.localTxid || 0), "backup-replication-local-txid"),
        detailCell("Remote TXID", String(status.remoteTxid || 0), "backup-replication-remote-txid"),
        detailCell("Last successful sync", formatTime(status.lastSuccessfulSyncAt), "backup-replication-last-sync"),
        status.assetMigrationRunning
            ? div({class: "md:col-span-3 text-amber-300 text-xs"},
                `${status.assetPending || 0} large asset(s) waiting to move ${status.assetTargetS3 ? "to S3" : "to local storage"}.`)
            : "",
        status.assetError ? div({class: "md:col-span-3 text-red-300 text-xs break-words"}, status.assetError) : "",
        status.error ? div({class: "md:col-span-3 text-red-300 text-xs break-words", "data-testid": "backup-replication-error"}, status.error) : "",
    );
}

function detailCell(label, value, testId) {
    return div(
        {class: "rounded border border-gray-800 bg-black/20 p-3"},
        div({class: "text-xs text-gray-500 mb-1"}, label),
        div({class: "text-gray-200 font-mono break-all", "data-testid": testId}, value),
    );
}

function secondaryInstallPanel(config, enrollmentInfo, configError, copied, onCopy) {
    return div(
        {class: "mt-4 pt-4 border-t border-gray-700"},
        div(
            {class: "flex items-center justify-between gap-3 mb-2"},
            div({class: "text-sm font-medium text-gray-200"}, "Install secondary command"),
            button(
                {
                    type: "button",
                    class: "btn-secondary text-sm py-1.5 px-3 shrink-0",
                    disabled: () => !secondaryInstallCommand(config.val?.config?.settings, enrollmentInfo.val, primaryOpenDeployVersion()),
                    onclick: onCopy,
                },
                () => copied.val ? "Copied" : "Copy",
            ),
        ),
        () => configError.val
            ? p({class: "text-xs text-red-400"}, configError.val)
            : installCommandBlock(secondaryInstallCommand(config.val?.config?.settings, enrollmentInfo.val, primaryOpenDeployVersion())),
    );
}

function installCommandBlock(command) {
    if (!command) {
        return p({class: "text-xs text-gray-500"}, "Loading primary version, cluster addresses, and enrollment fingerprint...");
    }
    return code({class: "app-scroll-x block overflow-x-auto whitespace-pre rounded bg-gray-950 p-3 text-xs text-gray-200"}, command);
}

function primaryOpenDeployVersion() {
    const primaryID = Number(machinesS.val.find(machine => machine.isPrimary)?.id || 0);
    if (!primaryID) return "";
    const deployment = deploymentsS.val.find(item =>
        Number(item.config?.nodeId || 0) === primaryID &&
        Number(item.config?.identity?.spaceId || 0) === 0 &&
        item.config?.identity?.name === "opendeploy",
    );
    return (deployment?.status?.runner?.runningVersion || deployment?.config?.desiredState?.version || "").trim();
}

function secondaryInstallCommand(config, enrollmentInfo, version) {
    const clusterListen = resolveStringSetting(config?.cluster?.listen);
    const enrollmentListen = resolveStringSetting(config?.cluster?.enrollmentListen);
    const enrollmentFingerprint = (enrollmentInfo?.enrollmentTlsSpkiSha256 || "").trim();
    if (!clusterListen || !enrollmentListen || !enrollmentFingerprint || !version) return "";
    const clusterAddr = dialAddress(clusterListen);
    const enrollmentAddr = dialAddress(enrollmentListen);
    if (!clusterAddr || !enrollmentAddr) return "";
    return `curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_secondary.sh | bash -s -- \\
   --cluster-addr "${clusterAddr}" \\
   --enrollment-addr "${enrollmentAddr}" \\
   --enrollment-fingerprint "${enrollmentFingerprint}" \\
   --version "${version}"`;
}

function resolveStringSetting(setting) {
    if (!setting) return "";
    const refID = Number(setting.configRef?.id || 0);
    if (!refID) return (setting.value || "").trim();
    const item = (userConfigsS.val || []).find(cfg => Number(cfg.id || 0) === refID);
    return (item?.value || "").trim();
}

function dialAddress(listen) {
    const value = (listen || "").trim();
    if (!value) return "";

    const port = listenPort(value);
    if (value.startsWith(":") || value.startsWith("0.0.0.0:") || value.startsWith("[::]:")) {
        return hostPort(browserHost(), port);
    }
    return value;
}

function listenPort(value) {
    const bracketPort = value.match(/^\[[^\]]+\]:(\d+)$/);
    if (bracketPort) return bracketPort[1];
    const hostPortMatch = value.match(/:(\d+)$/);
    return hostPortMatch ? hostPortMatch[1] : "";
}

function browserHost() {
    if (typeof window === "undefined") return "";
    return window.location.hostname;
}

function hostPort(host, port) {
    if (!host || !port) return "";
    const formattedHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
    return `${formattedHost}:${port}`;
}

function enrollmentRow(req) {
    const workerName = van.state(`worker-${req.id}`);
    const accepting = van.state(false);
    const rowError = van.state(null);

    const accept = async () => {
        const name = workerName.val.trim();
        if (!name) {
            rowError.val = "Worker name is required";
            return;
        }
        accepting.val = true;
        rowError.val = null;
        try {
            await capi.postV1EnrollmentAccept({id: req.id, workerName: name});
        } catch (e) {
            rowError.val = e.message;
        } finally {
            accepting.val = false;
        }
    };

    return tr(
        {class: "border-b border-gray-800 last:border-0 align-top", "data-testid": `enrollment-request-${req.id}`},
		td({class: "py-3 pr-6"},
			div({class: "text-white font-medium"}, `#${req.id}`),
			div({class: "text-xs text-gray-500 font-mono break-all"}, req.requestingMachineId),
			req.opendeployVersion ? div({class: "text-xs text-gray-400 font-mono"}, req.opendeployVersion) : '',
		),
        td({class: "py-3 pr-6 text-gray-300"}, req.requestingIpAddress || "-"),
        td({class: "py-3 pr-6 text-gray-400"}, formatTime(req.updatedAt)),
        td({class: "py-3"},
            div({class: "flex flex-col gap-2"},
                div({class: "flex gap-2"},
                    input({
                        "data-testid": "enrollment-worker-name-input",
                        class: "text-input w-44",
                        value: workerName,
                        oninput: e => workerName.val = e.target.value,
                    }),
                    button({
                        type: "button",
                        "data-testid": "enrollment-accept-button",
                        class: "btn-primary",
                        disabled: () => accepting.val,
                        onclick: accept,
                    }, () => accepting.val ? "Accepting..." : "Accept"),
                ),
				() => rowError.val ? p({class: "text-xs text-red-400"}, rowError.val) : '',
			)
		),
    );
}
