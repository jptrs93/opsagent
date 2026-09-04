import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {sectionBand} from "../components/sectionBand.js";
import {backupStatusS, deploymentsS, deploymentsStreamS, enrollmentsS, machinesS, primaryConfigS, spacesS, userConfigRefsS} from "../state/deployments.js";
import {deploymentWorkload} from "../lib/deployment.js";
import {allowedSpaceNames, editableSpaceIDs, isFixedSpace} from "../lib/nodeSpaces.js";

const { button, code, div, input, label, p, span, table, tbody, td, th, thead, tr } = van.tags;

const NODE_ENROLLMENT_REQUESTED = 1;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '-';
    return d.toLocaleString();
};

const headerCell = (text, cls = "pr-3") => th(
    {class: `py-1.5 ${cls} text-[10px] font-semibold uppercase tracking-wider`}, text);

export function clusterPage() {
    const config = primaryConfigS;
    const enrollmentInfo = van.state(null);
    const configError = van.state(null);
    const copied = van.state(false);
    const open = {nodes: van.state(true), backup: van.state(true), enrollments: van.state(true), install: van.state(true)};

    const loadEnrollmentInfo = async () => {
        try {
            configError.val = null;
            enrollmentInfo.val = await capi.getV1NodesEnrollmentsInfo();
        } catch (e) {
            configError.val = e.message || "Failed to load cluster config";
        }
    };
    loadEnrollmentInfo();

    const installCommand = () => secondaryInstallCommand(config.val?.config?.settings, enrollmentInfo.val, primaryOpenDeployVersion());

    const copyInstallCommand = async () => {
        const command = installCommand();
        if (!command) return;
        await navigator.clipboard.writeText(command);
        copied.val = true;
        setTimeout(() => copied.val = false, 2000);
    };

    const copyButton = button({
        type: "button",
        class: "inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-gray-600 text-gray-300 " +
            "hover:bg-surface-hover cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed",
        disabled: () => !installCommand(),
        onclick: copyInstallCommand,
    }, () => copied.val ? "Copied" : "Copy");

    const nodesSection = (sorted) => sorted.length === 0
        ? p({class: "px-4 py-2 text-gray-400 text-sm"}, "No nodes found.")
        : div({class: "pl-4 pr-2"}, table(
            {class: "w-full text-sm"},
            thead(tr({class: "text-left text-gray-500 border-b border-gray-800"},
                headerCell("Node", "pr-3 w-[24rem]"),
                headerCell("Role"),
                headerCell("Address"),
                headerCell("Host addresses"),
                headerCell("Transport key"),
                headerCell("Spaces"),
                headerCell("Status"),
                headerCell("Connected since", ""))),
            tbody(...sorted.map(machineRow))));

    const backupSection = () => div(
        {class: "px-4 py-2.5 flex flex-col gap-3", "data-testid": "backup-replication-card"},
        () => backupStatusDetails(backupStatusS.val));

    const enrollmentsSection = (pending) => div(
        {class: "pl-4 pr-2 flex flex-col gap-1"},
        p({class: "pt-1.5 text-sm text-gray-400"}, "Accept a waiting secondary to issue its cluster client certificate."),
        pending.length === 0
            ? p({class: "pb-2 text-gray-400 text-sm"}, "No pending enrollment requests.")
            : table(
                {class: "w-full text-sm"},
                thead(tr({class: "text-left text-gray-500 border-b border-gray-800"},
                    headerCell("Request", "pr-6"),
                    headerCell("Remote IP", "pr-6"),
                    headerCell("Requested", "pr-6"),
                    headerCell("Accept as", ""))),
                tbody(...pending.map(req => enrollmentRow(req)))));

    const installSection = () => div(
        {class: "px-4 py-2.5"},
        () => configError.val
            ? p({class: "text-xs text-red-400"}, configError.val)
            : installCommandBlock(installCommand()));

    return div(
        // bg-surface: like the assets and secrets explorers, the page is one
        // flush surface running to the window and sidebar edges.
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        div(
            // No app-scroll: its stable scrollbar gutter would hold the
            // section bands 8px off the right edge (the IAM page scrolls the
            // same way).
            {class: "flex-1 flex flex-col min-w-0 min-h-0 overflow-y-auto"},
            () => {
                if (deploymentsStreamS.val.status !== "connected" && machinesS.val.length === 0) {
                    return p({class: "px-4 py-3 text-gray-400"}, "Loading...");
                }

                const sorted = [...machinesS.val].sort((a, b) => {
                    if (a.isPrimary && !b.isPrimary) return -1;
                    if (!a.isPrimary && b.isPrimary) return 1;
                    return a.name.localeCompare(b.name);
                });
                const pending = enrollmentsS.val.filter(e => e.status === NODE_ENROLLMENT_REQUESTED && e.isConnected);

                return div(
                    {class: "flex flex-col"},
                    sectionBand(open.nodes, "Connected nodes", String(sorted.length)),
                    !open.nodes.val ? "" : nodesSection(sorted),
                    sectionBand(open.backup, "Backup replication", null,
                        () => backupStatusBadge(backupStatusS.val)),
                    !open.backup.val ? "" : backupSection(),
                    sectionBand(open.enrollments, "Enrollment requests", String(pending.length)),
                    !open.enrollments.val ? "" : enrollmentsSection(pending),
                    sectionBand(open.install, "Install secondary command", null, copyButton),
                    !open.install.val ? "" : installSection(),
                );
            },
        ),
    );
}

// The allow list, shown as names and edited as a set of checkboxes. A node
// starts out allowing every space, so in the common case this reads as "all"
// and the operator never opens it.
function allowedSpacesCell(machine) {
    const open = van.state(false);
    const saving = van.state(false);
    const error = van.state("");
    // Populated when the popover opens rather than kept in sync with the
    // stream, so an update arriving mid-edit does not silently rewrite the
    // boxes under the operator.
    const draft = van.state(new Set());

    const summary = () => {
        const names = allowedSpaceNames(machine, spacesS.val);
        const total = (spacesS.val || []).length;
        if (total && names.length >= total) return "all";
        return names.join(", ") || "-";
    };

    const toggle = (spaceID, checked) => {
        const next = new Set(draft.val);
        if (checked) next.add(Number(spaceID));
        else next.delete(Number(spaceID));
        draft.val = next;
    };

    const save = async () => {
        if (saving.val) return;
        saving.val = true;
        error.val = "";
        try {
            await capi.postV1NodesAllowedSpaces({
                identifier: machine.identifier,
                spaceIds: Array.from(draft.val),
            });
            open.val = false;
        } catch (e) {
            error.val = e.message || "Failed to update spaces";
        } finally {
            saving.val = false;
        }
    };

    return div({class: "flex flex-col gap-1"},
        div({class: "flex items-center gap-2"},
            span({class: "text-gray-300"}, summary),
            button({
                type: "button",
                class: "text-xs text-brand hover:text-blue-300 hover:underline cursor-pointer",
                "aria-label": `Edit allowed spaces for ${machine.identifier}`,
                onclick: () => {
                    if (open.val) { open.val = false; return; }
                    draft.val = new Set(editableSpaceIDs(machine));
                    error.val = "";
                    open.val = true;
                },
            }, () => open.val ? "Cancel" : "Edit"),
        ),
        () => !open.val ? "" : div(
            {class: "flex flex-col gap-1.5 rounded-md border border-gray-700 bg-gray-900 p-2"},
            ...[...(spacesS.val || [])].sort((a, b) => Number(a.id) - Number(b.id)).map((space) => {
                const fixed = isFixedSpace(space.id);
                return label({class: "flex items-center gap-2 text-xs text-gray-300"},
                    input({
                        type: "checkbox",
                        class: "accent-blue-500",
                        // Always allowed, and the server would add it back
                        // regardless, so it is shown ticked and locked rather
                        // than as a choice that silently does not take.
                        checked: () => fixed || draft.val.has(Number(space.id)),
                        disabled: () => fixed || saving.val,
                        onchange: (e) => toggle(space.id, e.target.checked),
                    }),
                    space.name || `space ${space.id}`,
                    fixed ? span({class: "text-gray-600"}, "(always)") : "",
                );
            }),
            div({class: "flex items-center gap-2 pt-1"},
                button({
                    type: "button",
                    disabled: saving,
                    class: () => `text-xs px-2.5 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600 ${
                        saving.val ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
                    onclick: save,
                }, () => saving.val ? "Saving..." : "Save"),
            ),
            () => error.val ? p({class: "text-xs text-red-400"}, error.val) : "",
        ),
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
            await capi.postV1NodesRename({identifier: machine.identifier, name: nextName});
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
                inputClass: "w-full min-w-36 bg-transparent px-2 py-1 rounded border border-transparent hover:border-gray-700 focus:border-brand focus:outline-none font-mono",
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
        td({class: "py-1 pr-3 font-mono text-gray-300"}, (machine.addresses || []).join(", ") || "-"),
        td({class: "py-1 pr-3 font-mono text-gray-300", "data-testid": "node-host-addresses", title: "Addresses an ingress listen selector can publish on"},
            (machine.hostAddresses || []).join(", ") || "-"),
        td({class: "py-1 pr-3 font-mono text-gray-300", title: machine.wgPublicKey || "No WireGuard key registered"},
            machine.wgPublicKey ? machine.wgPublicKey.slice(0, 8) + "…" : "-"),
        td({class: "py-1 pr-3"}, allowedSpacesCell(machine)),
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

function backupStatusBadge(status) {
    const label = backupStatusLabel(status);
    const klass = status?.error || status?.assetError
        ? "bg-red-950 text-red-300 border-red-800"
        : status?.inSync && !status?.assetMigrationRunning
            ? "bg-green-950 text-green-300 border-green-800"
            : status?.configured || status?.assetMigrationRunning
                ? "bg-yellow-950 text-yellow-300 border-yellow-800"
                : "bg-gray-800 text-gray-300 border-gray-700";
    return span({class: klass + " px-2 py-0.5 rounded border text-xs font-medium", "data-testid": "backup-replication-status"}, label);
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
        Number(item.config?.def?.nodeId || 0) === primaryID &&
        Number(item.config?.def?.spaceId || 0) === 0 &&
        item.config?.def?.name === "opendeploy",
    );
    return (deployment?.status?.runner?.runningVersion || deploymentWorkload(deployment?.config)?.version || "").trim();
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
    const refID = Number(setting.configRef?.versionId || 0);
    if (!refID) return (setting.value || "").trim();
    const item = (userConfigRefsS.val || []).find(ref => Number(ref.id || 0) === refID);
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

// Rows are recreated on every enrollment stream update, so a name the user is
// typing must survive the re-render or a late update silently reverts the
// accept to the default name.
const enrollmentNameDrafts = new Map();

function enrollmentRow(req) {
    const nodeName = van.state(enrollmentNameDrafts.get(req.id) ?? `node-${req.id}`);
    const accepting = van.state(false);
    const rowError = van.state(null);

    const accept = async () => {
        const name = nodeName.val.trim();
        if (!name) {
            rowError.val = "Node name is required";
            return;
        }
        accepting.val = true;
        rowError.val = null;
        try {
            await capi.postV1NodesEnrollmentsAccept({id: req.id, nodeName: name});
            enrollmentNameDrafts.delete(req.id);
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
        td({class: "py-3 pr-6 text-gray-400"}, formatTime(req.createdAt)),
        td({class: "py-3"},
            div({class: "flex flex-col gap-2"},
                div({class: "flex gap-2"},
                    input({
                        "data-testid": "enrollment-node-name-input",
                        class: "text-input w-44",
                        value: nodeName,
                        oninput: e => {
                            nodeName.val = e.target.value;
                            enrollmentNameDrafts.set(req.id, e.target.value);
                        },
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
