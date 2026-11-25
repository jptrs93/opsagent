// The Nodes page: a tab strip in the Users & roles style with Nodes (a dense
// table and a right-hand inspector for the selected node), Access policies
// (which nodes may run which spaces), Enrollments (pending requests and the
// secondary install command) and Replication (the backup status). The fixture in fixtures/nodes-redesign mounts this page over
// an in-memory node API.
import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {evictNodeOverlay, pinnedUserDeployments} from "../components/evictNodeOverlay.js";
import {explorerBand} from "../components/sectionBand.js";
import {tabButton, tabPanel, tabStrip} from "../components/tabs.js";
import {backupStatusS, deploymentsS, deploymentsStreamS, enrollmentsS, machinesS, nodeAccessPoliciesS, systemConfigS, spacesS, userConfigRefsS} from "../state/deployments.js";
import {deploymentWorkload, isSelfDeployment, placementNodeId} from "../lib/deployment.js";
import {allowedSpaceNames, runsEverySpace} from "../lib/nodeSpaces.js";
import {databaseSizeHint, databaseSizeText} from "../lib/backupStatus.js";
import {nodeAccessPoliciesPage} from "./nodeAccessPolicies.js";
import {formatDateTime} from "../lib/date.js";
import {checkIcon, closeIcon, copyIcon, searchIcon} from "../lib/icons.js";

const {button, code, col, colgroup, div, input, p, span, table, tbody, td, th, thead, tr} = van.tags;

const NODES = "nodes";
const ACCESS = "access";
const ENROLLMENTS = "enrollments";
const REPLICATION = "replication";
const NODE_ENROLLMENT_REQUESTED = 1;
const INSPECTOR_MIN = 260;
const INSPECTOR_MAX = 560;

// --- formatting ---------------------------------------------------------------

const asDate = (t) => {
    if (!t) return null;
    const d = t instanceof Date ? t : new Date(t);
    return isNaN(d.getTime()) || d.getTime() <= 0 ? null : d;
};

const relativeTime = (t) => {
    const d = asDate(t);
    if (!d) return "-";
    const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
    if (s < 60) return "just now";
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
};

const absoluteTime = (t) => {
    const d = asDate(t);
    return d ? `${formatDateTime(d)} (${d.toLocaleString()})` : "";
};

// One item shows as itself; more collapse to a count so the row stays one
// line. The full list rides on the cell's title.
const listSummary = (items, noun) => {
    const list = items || [];
    if (list.length === 0) return "-";
    if (list.length === 1) return list[0];
    return `${list.length} ${noun}`;
};

const spacesSummary = (machine) => {
    const names = allowedSpaceNames(machine, spacesS.val);
    if (runsEverySpace(machine, spacesS.val)) return "all";
    if (names.length === 1) return names[0];
    return names.length === 0 ? "-" : `${names.length} spaces`;
};

const runtimeSummary = (versions) => (versions || "").replace(", ", " · ") || "-";

const HOST_ADDRESSES_UNKNOWN = "The node could not enumerate its host addresses";

const hostAddressesSummary = (machine) => machine.hostAddressesUnknown && !(machine.hostAddresses || []).length
    ? span({class: "text-amber-300/80", title: HOST_ADDRESSES_UNKNOWN}, "unknown")
    : span({title: (machine.hostAddresses || []).join("\n") || "No host addresses reported"}, listSummary(machine.hostAddresses, "addresses"));

const nodeState = (machine) => machine.evicted ? "evicted" : machine.connected ? "connected" : "disconnected";

const statusDot = (machine) => span({
    class: `inline-block h-1.5 w-1.5 flex-none rounded-full ${
        machine.evicted ? "bg-gray-600" : machine.connected ? "bg-green-400" : "bg-red-400"}`,
});

const statusText = (machine) => span(
    {class: "cluster-tone", "data-tone": machine.evicted ? "off" : machine.connected ? "ok" : "error"},
    nodeState(machine));

// In the table the draining word replaces the connection word rather than
// sitting beside it, so the cell stays one line when the inspector narrows
// the table; the dot still carries the connection state.
const compactStatus = (machine) => machine.draining
    ? span({class: "cluster-tone", "data-tone": "warn", title: `${nodeState(machine)}, draining`, "data-testid": "node-draining-badge"}, "draining")
    : statusText(machine);

const drainingTag = () => span(
    {class: "flex-none rounded bg-amber-500/15 px-1 py-px text-[10px] font-medium uppercase tracking-wide text-amber-300 graphite:bg-[#d8b36a]/10 graphite:text-[#d8b36a]", "data-testid": "node-draining-badge"},
    "draining");

const roleTag = (machine) => machine.isPrimary
    ? span({class: "flex-none rounded bg-blue-500/15 px-1 py-px text-[10px] font-medium uppercase tracking-wide text-blue-300 graphite:bg-white/[0.06] graphite:text-gray-400"}, "primary")
    : "";

// --- page ---------------------------------------------------------------------

export function clusterPage() {
    const activeTab = van.state(NODES);
    const search = van.state("");
    const showEvicted = van.state(false);
    const selectedId = van.state(null);
    const inspectorWidth = van.state(340);
    const enrollmentInfo = van.state(null);
    const configError = van.state(null);
    const overlay = van.state(null);
    const open = {pending: van.state(true), install: van.state(true)};

    const loadEnrollmentInfo = async () => {
        try {
            configError.val = null;
            enrollmentInfo.val = await capi.getV1NodesEnrollmentsInfo();
        } catch (e) {
            configError.val = e.message || "Failed to load cluster config";
        }
    };
    loadEnrollmentInfo();

    const liveNodes = () => machinesS.val.filter((m) => !m.evicted);
    const evictedNodes = () => machinesS.val.filter((m) => m.evicted);
    const pendingEnrollments = () => enrollmentsS.val.filter((e) => e.status === NODE_ENROLLMENT_REQUESTED && e.isConnected);

    const sortedNodes = () => {
        const q = search.val.trim().toLowerCase();
        const matches = (m) => !q || [m.name, m.identifier, ...(m.addresses || []), ...(m.hostAddresses || []), nodeState(m)]
            .some((v) => String(v || "").toLowerCase().includes(q));
        const live = liveNodes().filter(matches).sort((a, b) => {
            if (a.isPrimary !== b.isPrimary) return a.isPrimary ? -1 : 1;
            return a.name.localeCompare(b.name);
        });
        const evicted = showEvicted.val ? evictedNodes().filter(matches).sort((a, b) => b.eventTime - a.eventTime) : [];
        return [...live, ...evicted];
    };

    const selectedMachine = () => machinesS.val.find((m) => m.id === selectedId.val) || null;

    // --- shared pieces ----------------------------------------------------------

    const headerRow = (...labels) => thead(tr(
        {class: "cluster-head-row"},
        ...labels.map(([text, cls]) => th({
            class: `cluster-th col-head classic:text-[10px] ${cls || ""}`,
        }, text)),
    ));

    const emptyLine = (text) => p({class: "px-3 py-3 text-sm text-gray-400"}, text);

    const searchBox = (state, placeholder) => div({class: "relative"},
        searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
        input({
            class: "text-input search-input search-input-iconed toolbar-input",
            type: "search",
            placeholder,
            "aria-label": placeholder,
            value: state,
            oninput: (e) => { state.val = e.target.value; },
        }));

    const toolbar = (...children) => div(
        {class: "page-toolbar cluster-toolbar gap-2 classic:py-2"},
        ...children,
    );

    const toggleButton = (text, state, attrs = {}) => button({
        type: "button",
        class: () => `toolbar-button ${state.val ? "border-gray-400 bg-white/5 text-gray-100" : ""}`,
        "aria-pressed": () => String(state.val),
        onclick: () => { state.val = !state.val; },
        ...attrs,
    }, text);

    const copyButton = (text, {label = "Copy", small = false} = {}) => {
        const copied = van.state(false);
        return button({
            type: "button",
            title: label,
            "aria-label": label,
            class: small
                ? "inline-flex h-5 w-5 flex-none items-center justify-center rounded text-gray-500 hover:bg-white/10 hover:text-gray-100 cursor-pointer"
                : "cluster-btn inline-flex items-center gap-1 px-2 py-0.5",
            disabled: () => !text(),
            onclick: async () => {
                const value = text();
                if (!value) return;
                await navigator.clipboard.writeText(value);
                copied.val = true;
                setTimeout(() => { copied.val = false; }, 1600);
            },
        }, () => small
            ? (copied.val ? checkIcon({class: "h-3 w-3 text-green-400"}) : copyIcon({class: "h-3 w-3"}))
            : span(copied.val ? "Copied" : label));
    };

    // --- nodes tab ------------------------------------------------------------

    const nodeRow = (machine) => {
        const selected = () => selectedId.val === machine.id;
        const muted = machine.evicted;
        const cell = (cls, ...children) => td({class: `truncate py-1 pr-3 ${cls}`}, ...children);
        return tr(
            {
                class: () => `data-row cursor-pointer align-middle classic:border-gray-800/80 classic:last:border-0 ${
                    selected() ? "bg-white/5" : "classic:hover:bg-white/3"} ${muted ? "text-gray-500" : ""}`,
                tabindex: 0,
                "aria-selected": () => String(selected()),
                "data-testid": `machine-row-${machine.identifier}`,
                onclick: () => { selectedId.val = selected() ? null : machine.id; },
                onkeydown: (e) => {
                    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selectedId.val = machine.id; }
                },
            },
            td({class: () => `truncate py-1 pl-3 pr-3 ${selected() ? "shadow-[inset_2px_0_0_var(--color-brand)]" : ""}`},
                div({class: "flex min-w-0 items-center gap-1.5"},
                    span({class: `truncate font-mono ${muted ? "text-gray-500" : "text-gray-100"}`, title: machine.identifier, "data-testid": "node-name"}, machine.name || machine.identifier),
                    roleTag(machine))),
            cell("", div({class: "flex min-w-0 items-center gap-1.5"}, statusDot(machine), compactStatus(machine))),
            cell("font-mono text-gray-300", span({title: (machine.addresses || []).join("\n")}, listSummary(machine.addresses, "addresses"))),
            cell("font-mono text-gray-300", div({"data-testid": "node-host-addresses"}, hostAddressesSummary(machine))),
            cell("text-gray-300", span({title: allowedSpaceNames(machine, spacesS.val).join(", ")}, machine.evicted ? "-" : spacesSummary(machine))),
            cell(`font-mono text-gray-400 ${muted ? "" : "slate:text-[#5fcfc0] graphite:text-gray-300"}`, span({title: machine.opendeployVersion ? `Agent ${machine.opendeployVersion}` : "", "data-testid": "node-version"}, machine.opendeployVersion || "-")),
            cell("font-mono text-gray-400", span({title: machine.runtimeVersions || "", "data-testid": "node-runtime-versions"}, runtimeSummary(machine.runtimeVersions))),
            cell("text-gray-400 tabular-nums", span({title: machine.evicted ? absoluteTime(machine.eventTime) : absoluteTime(machine.connectedAt)},
                machine.isPrimary ? "-" : machine.evicted ? `evicted ${relativeTime(machine.eventTime)}` : machine.connected ? relativeTime(machine.connectedAt) : "-")),
        );
    };

    const nodesTable = () => {
        const rows = sortedNodes();
        if (!rows.length) return emptyLine(search.val.trim() ? "No nodes match your search." : "No nodes found.");
        return table({class: "w-full table-fixed text-[13px]"},
            colgroup(
                col({style: "width:17%"}), col({style: "width:12%"}), col({style: "width:15%"}), col({style: "width:12%"}),
                col({style: "width:7%"}), col({style: "width:8%"}), col({style: "width:18%"}), col({style: "width:11%"})),
            headerRow(["Node", "pl-3"], ["Status"], ["Address"], ["Host addresses"], ["Spaces"], ["Version"], ["Runtime"], ["Connected"]),
            tbody(...rows.map(nodeRow)));
    };

    // --- inspector --------------------------------------------------------------

    const kvRow = (key, ...value) => div({class: "flex flex-col gap-0.5 px-3 py-1.5"},
        span({class: "col-head classic:text-[10px]"}, key),
        div({class: "min-w-0 text-xs text-gray-200"}, ...value));

    const monoValue = (value, {copy = true} = {}) => div({class: "flex items-start gap-1"},
        span({class: "min-w-0 break-all font-mono text-gray-200"}, value || "-"),
        copy && value ? copyButton(() => value, {label: "Copy", small: true}) : "");

    const monoList = (items, empty) => (items || []).length
        ? div({class: "flex flex-col gap-0.5"}, ...items.map((item) => monoValue(item)))
        : span({class: "text-gray-500"}, empty);

    const nameEditor = (machine) => {
        const originalName = van.state(machine.name || "");
        const name = van.state(machine.name || "");
        const saving = van.state(false);
        const error = van.state("");
        const rename = async () => {
            const next = name.val.trim();
            if (!next || saving.val) return;
            if (next === originalName.val) { name.val = originalName.val; return; }
            saving.val = true;
            error.val = "";
            try {
                await capi.postV1NodesRename({identifier: machine.identifier, name: next});
                originalName.val = next;
            } catch (e) {
                error.val = e.message || "Failed to rename node";
            } finally {
                saving.val = false;
            }
        };
        return div({class: "flex min-w-0 flex-col gap-0.5"},
            inlineEditableInput({
                value: name,
                dirty: () => name.val !== originalName.val,
                valid: () => Boolean(name.val.trim()),
                disabled: saving,
                oninput: (e) => { name.val = e.target.value; },
                onSave: rename,
                onDiscard: () => { name.val = originalName.val; },
                inputClass: "w-full min-w-0 bg-transparent px-1.5 py-0.5 -mx-1.5 rounded border border-transparent hover:border-gray-700 focus:border-brand focus:outline-none font-mono text-sm font-medium text-white",
                ariaLabel: `Node name for ${machine.identifier}`,
                saveAriaLabel: `Save node name ${machine.identifier}`,
                discardAriaLabel: `Discard node name change for ${machine.identifier}`,
            }),
            () => error.val ? p({class: "text-xs text-red-400"}, error.val) : "");
    };

    const spacesField = (machine) => {
        const names = allowedSpaceNames(machine, spacesS.val);
        return div({class: "flex flex-col gap-1"},
            div({class: "flex flex-wrap items-center gap-1", "data-testid": "node-inspector-spaces"},
                ...(runsEverySpace(machine, spacesS.val) ? [span({class: "text-gray-300"}, "all spaces")]
                    : names.length ? names.map((n) => span({class: "cluster-chip"}, n))
                        : [span({class: "text-gray-500"}, "none")])),
            p({class: "text-[11px] text-gray-500"},
                "Spaces whose deployments this node may run, as the ",
                button({
                    type: "button",
                    class: "text-brand hover:text-blue-300 hover:underline cursor-pointer",
                    "data-testid": "node-inspector-access-policies",
                    onclick: () => { activeTab.val = ACCESS; },
                }, "access policies"),
                " decide."));
    };

    const actionsRow = (machine) => {
        const busy = van.state(false);
        const error = van.state("");
        const toggleDrain = async () => {
            if (busy.val) return;
            busy.val = true;
            error.val = "";
            try {
                await capi.postV1NodesDrain({identifier: machine.identifier, draining: !machine.draining});
            } catch (e) {
                error.val = e?.message || "Updating node failed.";
            } finally {
                busy.val = false;
            }
        };
        const openEvict = () => {
            overlay.val = evictNodeOverlay({
                machine,
                pinned: pinnedUserDeployments(deploymentsS.val, machine.id),
                evict: (force) => capi.postV1NodesEvict({identifier: machine.identifier, expectedSeq: Number(machine.seq || 0), force}),
                close: () => { overlay.val = null; },
            });
        };
        return div({class: "flex flex-col gap-1.5 border-t border-gray-800 px-3 py-2.5 slate:border-line graphite:border-line"},
            div({class: "flex items-center gap-2"},
                button({
                    type: "button", disabled: busy,
                    class: "cluster-btn",
                    "data-testid": `node-drain-${machine.identifier}`,
                    title: machine.draining ? "Allow new deployments on this node again" : "Stop placing new deployments on this node",
                    onclick: toggleDrain,
                }, machine.draining ? "Undrain" : "Drain"),
                div({class: "flex-1"}),
                button({
                    type: "button", disabled: busy,
                    class: "cluster-btn cluster-btn-danger",
                    "data-testid": `node-evict-${machine.identifier}`,
                    title: "Permanently remove this node from the cluster",
                    onclick: openEvict,
                }, "Evict")),
            () => error.val ? p({class: "text-xs text-red-400"}, error.val) : "");
    };

    const inspectorBody = (machine) => {
        const header = div({class: "flex flex-col gap-1 border-b border-gray-800 px-3 pb-2.5 pt-3 pr-8 slate:border-line graphite:border-line"},
            machine.evicted
                ? span({class: "font-mono text-sm font-medium text-gray-400"}, machine.name || machine.identifier)
                : nameEditor(machine),
            div({class: "flex items-center gap-1.5 text-xs"},
                statusDot(machine), statusText(machine),
                machine.draining ? drainingTag() : "",
                roleTag(machine),
                machine.isPrimary ? "" : span({class: "text-gray-500"}, "secondary")));

        if (machine.evicted) {
            return [header,
                div({class: "app-scroll flex-1 min-h-0 overflow-y-auto py-1"},
                    kvRow("Identifier", monoValue(machine.identifier)),
                    kvRow("Evicted", span({title: absoluteTime(machine.eventTime)}, formatDateTime(asDate(machine.eventTime), "-"))),
                    p({class: "px-3 py-2 text-xs text-gray-500"}, "This node has been removed from the cluster and wiped. Its identifier stays listed so it cannot be re-enrolled by mistake."))];
        }

        return [header,
            div({class: "app-scroll flex-1 min-h-0 overflow-y-auto py-1"},
                kvRow("Identifier", monoValue(machine.identifier)),
                kvRow("Connected since", machine.isPrimary
                    ? span({class: "text-gray-500"}, "always (primary)")
                    : machine.connected
                        ? span({title: absoluteTime(machine.connectedAt)}, `${formatDateTime(asDate(machine.connectedAt), "-")} · ${relativeTime(machine.connectedAt)}`)
                        : span({class: "cluster-tone", "data-tone": "error"}, machine.connectedAt ? `last seen ${relativeTime(machine.connectedAt)}` : "never connected")),
                kvRow("Enrolled", span({title: absoluteTime(machine.enrolledAt)}, formatDateTime(asDate(machine.enrolledAt), "-"))),
                kvRow("Cluster address", monoList(machine.addresses, "No underlay address reported")),
                kvRow("Host addresses", div({"data-testid": "node-inspector-host-addresses"},
                    machine.hostAddressesUnknown && !(machine.hostAddresses || []).length
                        ? span({class: "text-amber-300/80"}, HOST_ADDRESSES_UNKNOWN)
                        : monoList(machine.hostAddresses, "No host addresses reported")),
                    p({class: "pt-1 text-[11px] text-gray-500"}, "Addresses an ingress listen selector can publish on.")),
                kvRow("Transport key", machine.wgPublicKey ? monoValue(machine.wgPublicKey) : span({class: "text-gray-500"}, "No WireGuard key registered")),
                kvRow("Agent version", machine.opendeployVersion
                    ? span({class: "font-mono"}, machine.opendeployVersion)
                    : span({class: "text-gray-500"}, "Not reported")),
                kvRow("Runtime", machine.runtimeVersions
                    ? span({class: "font-mono"}, machine.runtimeVersions)
                    : span({class: "text-gray-500"}, "Not reported")),
                kvRow("Spaces", spacesField(machine))),
            machine.isPrimary ? "" : actionsRow(machine)];
    };

    const startInspectorResize = (e) => {
        e.preventDefault();
        const startX = e.clientX;
        const startW = inspectorWidth.val;
        document.body.classList.add("resizing");
        const move = (ev) => {
            inspectorWidth.val = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, startW + (startX - ev.clientX)));
        };
        const up = () => {
            document.body.classList.remove("resizing");
            window.removeEventListener("mousemove", move);
            window.removeEventListener("mouseup", up);
        };
        window.addEventListener("mousemove", move);
        window.addEventListener("mouseup", up);
    };

    const inspector = () => div(
        {class: "relative flex flex-none flex-col border-l border-gray-700 bg-gray-950/35 slate:border-line slate:bg-transparent graphite:border-line graphite:bg-transparent", style: () => `width:${inspectorWidth.val}px`, "data-testid": "node-inspector"},
        span({
            class: "vgrip", tabindex: "0", role: "separator", "aria-orientation": "vertical", "aria-label": "Resize details pane",
            onmousedown: startInspectorResize,
            onkeydown: (e) => {
                if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
                e.preventDefault();
                const step = e.shiftKey ? 48 : 16;
                inspectorWidth.val = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, inspectorWidth.val + (e.key === "ArrowLeft" ? step : -step)));
            },
        }),
        button({
            type: "button", "aria-label": "Close details",
            class: "absolute right-1.5 top-1.5 z-[6] inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:bg-white/10 hover:text-gray-100 cursor-pointer",
            onclick: () => { selectedId.val = null; },
        }, closeIcon({class: "h-3.5 w-3.5"})),
        () => {
            const machine = selectedMachine();
            return machine
                ? div({class: "contents"}, ...inspectorBody(machine))
                : div({class: "px-3 py-6 text-xs text-gray-500"}, "Select a node to see its details.");
        },
    );

    const nodesPanel = div(
        {class: "flex h-full min-h-0 flex-col", "data-testid": "nodes-tab-panel"},
        toolbar(
            searchBox(search, "Search nodes"),
            div({class: "flex-1"}),
            () => evictedNodes().length
                ? toggleButton(`Evicted (${evictedNodes().length})`, showEvicted, {title: "Show nodes removed from the cluster"})
                : ""),
        div({class: "flex flex-1 min-h-0 min-w-0"},
            div({class: "app-scroll relative flex-1 min-h-0 min-w-0 overflow-y-auto"},
                () => deploymentsStreamS.val.status !== "connected" && machinesS.val.length === 0
                    ? p({class: "px-3 py-3 text-gray-400"}, "Loading...")
                    : nodesTable()),
            () => selectedId.val !== null ? inspector() : ""),
    );

    // --- enrollments tab ------------------------------------------------------

    const installCommand = () => secondaryInstallCommand(systemConfigS.val?.settings, enrollmentInfo.val, primaryOpenDeployVersion());

    const enrollmentNameDrafts = new Map();

    const enrollmentRow = (req) => {
        const nodeName = van.state(enrollmentNameDrafts.get(req.id) ?? `node-${req.id}`);
        const accepting = van.state(false);
        const rowError = van.state(null);
        const accept = async () => {
            const name = nodeName.val.trim();
            if (!name) { rowError.val = "Node name is required"; return; }
            accepting.val = true;
            rowError.val = null;
            try {
                await capi.postV1NodesEnrollmentsAccept({id: req.id, nodeName: name, expectedSeq: Number(req.seq || 0)});
                enrollmentNameDrafts.delete(req.id);
            } catch (e) {
                rowError.val = e.message;
            } finally {
                accepting.val = false;
            }
        };
        return tr(
            {class: "data-row align-middle classic:border-gray-800/80 classic:last:border-0 classic:hover:bg-transparent", "data-testid": `enrollment-request-${req.id}`},
            td({class: "truncate py-1.5 pl-3 pr-3"},
                div({class: "flex min-w-0 items-center gap-2"},
                    span({class: "flex-none text-gray-200"}, `#${req.id}`),
                    span({class: "truncate font-mono text-gray-400", title: req.requestingMachineId}, req.requestingMachineId))),
            td({class: "truncate py-1.5 pr-3 font-mono text-gray-300"}, req.requestingIpAddress || "-"),
            td({class: "truncate py-1.5 pr-3 font-mono text-gray-400"}, req.opendeployVersion || "-"),
            td({class: "truncate py-1.5 pr-3 text-gray-400 tabular-nums", title: absoluteTime(req.createdAt)}, relativeTime(req.createdAt)),
            td({class: "py-1 pr-3"},
                div({class: "flex items-center gap-2"},
                    input({
                        "data-testid": "enrollment-node-name-input",
                        class: "text-input toolbar-input w-44 text-xs",
                        value: nodeName,
                        "aria-label": `Name for enrollment request ${req.id}`,
                        oninput: (e) => { nodeName.val = e.target.value; enrollmentNameDrafts.set(req.id, e.target.value); },
                        onkeydown: (e) => { if (e.key === "Enter") accept(); },
                    }),
                    button({
                        type: "button", "data-testid": "enrollment-accept-button",
                        class: "toolbar-button border-brand/60 bg-brand/15 text-blue-100 hover:bg-brand/30",
                        disabled: () => accepting.val,
                        onclick: accept,
                    }, () => accepting.val ? "Accepting..." : "Accept"),
                    () => rowError.val ? span({class: "text-xs text-red-400"}, rowError.val) : "")),
        );
    };

    const pendingTable = () => {
        const pending = pendingEnrollments();
        if (!pending.length) return emptyLine("No pending enrollment requests.");
        return table({class: "w-full table-fixed text-[13px]"},
            colgroup(col({style: "width:30%"}), col({style: "width:14%"}), col({style: "width:10%"}), col({style: "width:12%"}), col({style: "width:34%"})),
            headerRow(["Request", "pl-3"], ["Remote IP"], ["Version"], ["Requested"], ["Accept as"]),
            tbody(...pending.map(enrollmentRow)));
    };

    const installBlock = () => {
        const command = installCommand();
        if (configError.val) return p({class: "px-3 py-2 text-xs text-red-400"}, configError.val);
        return div({class: "flex flex-col gap-2 px-3 py-2.5"},
            p({class: "text-xs text-gray-400"},
                "Run this on the machine you want to add. It installs the agent, connects to the enrollment listener and waits here under Pending requests until it is accepted."),
            div({class: "relative"},
                command
                    ? code({class: "app-scroll-x block overflow-x-auto whitespace-pre rounded bg-code p-3 pr-20 text-xs text-gray-200"}, command)
                    : p({class: "rounded bg-code p-3 text-xs text-gray-500"}, "Loading primary version, cluster addresses, and enrollment fingerprint..."),
                div({class: "absolute right-2 top-2"}, copyButton(installCommand, {label: "Copy"}))));
    };

    const accessPanel = nodeAccessPoliciesPage();

    const enrollmentsPanel = div(
        {class: "flex h-full min-h-0 flex-col", "data-testid": "enrollments-tab-panel"},
        div({class: "app-scroll flex flex-1 min-h-0 flex-col overflow-y-auto"},
            explorerBand(open.pending, "Pending requests", () => String(pendingEnrollments().length)),
            () => open.pending.val ? div({class: "flex flex-col"},
                p({class: "px-3 pt-2 pb-1 text-xs text-gray-500"}, "Accepting a request names the node and issues its cluster client certificate."),
                pendingTable) : "",
            explorerBand(open.install, "Install a secondary", ""),
            () => open.install.val ? installBlock() : ""),
    );

    // --- replication tab ----------------------------------------------------------

    const tile = (labelText, value, {tone = "", testId = "", hint = ""} = {}) => div(
        {class: "cluster-tile"},
        span({class: "col-head classic:text-[10px]"}, labelText),
        span({class: `cluster-tone font-mono text-sm ${tone ? "" : "text-gray-200"}`, "data-tone": tone, "data-testid": testId, title: hint}, value));

    const replicationBody = () => {
        const status = backupStatusS.val;
        const configured = Boolean(status?.configured);
        const sizeTile = () => tile("Database size", databaseSizeText(status),
            {tone: status?.dbSizeBytes ? "" : "off", testId: "backup-replication-db-size", hint: databaseSizeHint(status)});
        if (!configured && !assetsSyncing(status) && !status?.assetError) {
            return div({class: "flex flex-col gap-3 px-3 py-3", "data-testid": "backup-replication-unconfigured"},
                div({class: "flex flex-col gap-2 py-1"},
                    p({class: "text-sm text-gray-300"}, "Backups are not configured."),
                    p({class: "text-xs text-gray-500"}, "Set a backup target under Settings to replicate the primary database and assets off this node.")),
                div({class: "grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-5"}, sizeTile()));
        }
        const lag = Math.max(0, Number(status.localTxid || 0) - Number(status.remoteTxid || 0));
        const lastSync = asDate(status.lastSuccessfulSyncAt);
        return div({class: "flex flex-col gap-3 px-3 py-3", "data-testid": "backup-replication-card"},
            div({class: "flex flex-wrap items-center gap-2"},
                backupStatusBadge(status),
                span({class: "text-xs text-gray-400"}, backupStatusSentence(status))),
            div({class: "grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-5"},
                tile("Local TXID", String(status.localTxid || 0), {testId: "backup-replication-local-txid"}),
                tile("Remote TXID", String(status.remoteTxid || 0), {testId: "backup-replication-remote-txid"}),
                tile("Lag", lag === 0 ? "0" : `${lag} tx`, {tone: lag === 0 ? "ok" : "warn", hint: "Transactions the remote copy is behind the primary"}),
                tile("Last successful sync", lastSync ? `${formatDateTime(lastSync)} · ${relativeTime(lastSync)}` : "never",
                    {tone: lastSync ? "" : "off", testId: "backup-replication-last-sync", hint: absoluteTime(lastSync)}),
                sizeTile()),
            assetsSyncing(status)
                ? div({class: "rounded border border-amber-900/60 bg-amber-950/30 px-3 py-2 text-xs text-amber-200 slate:rounded-md slate:border-amber-500/25 slate:bg-amber-500/[0.07] graphite:border-[#d8b36a]/30 graphite:bg-[#d8b36a]/[0.06] graphite:text-[#d8b36a]"},
                    `${status.assetPending || 0} asset(s) ${assetPendingDescription(status)}.`)
                : "",
            status.assetError
                ? div({class: "rounded border border-red-900/60 bg-red-950/30 px-3 py-2 text-xs text-red-200 break-words slate:rounded-md slate:border-red-500/30 slate:bg-red-500/[0.08] graphite:border-red-500/30 graphite:bg-red-500/[0.06]"},
                    span({class: "font-semibold"}, "Asset sync: "), status.assetError)
                : "",
            status.error
                ? div({class: "rounded border border-red-900/60 bg-red-950/30 px-3 py-2 text-xs text-red-200 break-words slate:rounded-md slate:border-red-500/30 slate:bg-red-500/[0.08] graphite:border-red-500/30 graphite:bg-red-500/[0.06]", "data-testid": "backup-replication-error"},
                    span({class: "font-semibold"}, "Database replication: "), status.error)
                : "");
    };

    const replicationPanel = div(
        {class: "flex h-full min-h-0 flex-col", "data-testid": "replication-tab-panel"},
        div({class: "app-scroll flex flex-1 min-h-0 flex-col overflow-y-auto"}, replicationBody),
    );

    // --- tab strip ------------------------------------------------------------

    const replicationDot = () => span({
        class: () => `inline-block h-1.5 w-1.5 rounded-full ${backupToneDot(backupStatusS.val)}`,
        title: () => backupStatusLabel(backupStatusS.val),
    });

    const strip = tabStrip({label: "Nodes"},
        tabButton({id: NODES, activeTab, title: "Nodes", testid: `nodes-tab-${NODES}`, count: () => String(liveNodes().length)}),
        tabButton({id: ACCESS, activeTab, title: "Access policies", testid: `nodes-tab-${ACCESS}`, count: () => String(nodeAccessPoliciesS.val.length)}),
        tabButton({id: ENROLLMENTS, activeTab, title: "Enrollments", testid: `nodes-tab-${ENROLLMENTS}`, count: () => String(pendingEnrollments().length)}),
        tabButton({id: REPLICATION, activeTab, title: "Replication", testid: `nodes-tab-${REPLICATION}`, trailing: replicationDot()}),
    );

    const panel = (id, content) => tabPanel(activeTab, id, content);

    return div(
        {class: "page", "data-testid": "nodes-page"},
        strip,
        div({class: "page-body"},
            panel(NODES, nodesPanel),
            panel(ACCESS, accessPanel),
            panel(ENROLLMENTS, enrollmentsPanel),
            panel(REPLICATION, replicationPanel)),
        () => overlay.val || "",
    );
}

// --- backup status ------------------------------------------------------------

function assetsSyncing(status) {
    return (status?.assetPending || 0) > 0;
}

function assetSyncLabel(status) {
    if (status.assetKeepLocal) return "syncing assets to S3 and local";
    return status.assetTargetS3 ? "moving assets to S3" : "moving assets local";
}

function assetPendingDescription(status) {
    if (status.assetKeepLocal) return "waiting to sync between S3 and local storage";
    return status.assetTargetS3 ? "waiting to move to S3" : "waiting to move to local storage";
}

function backupStatusLabel(status) {
    if (status?.error || status?.assetError) return "error";
    if (assetsSyncing(status)) return assetSyncLabel(status);
    if (!status || !status.configured) return "not configured";
    if (!status.running) return "not running";
    if (status.inSync) return "in sync";
    return "syncing";
}

function backupStatusSentence(status) {
    if (status?.error || status?.assetError) return "Replication is reporting an error; the details are below.";
    if (assetsSyncing(status)) return "Assets are still being moved to their configured target.";
    if (!status?.running) return "Backups are configured but the replication worker is not running.";
    if (status.inSync) return "The remote copy matches the primary database.";
    return "The remote copy is catching up with the primary database.";
}

function backupToneDot(status) {
    if (status?.error || status?.assetError) return "bg-red-400";
    if (status?.inSync && !assetsSyncing(status)) return "bg-green-400";
    if (status?.configured || assetsSyncing(status)) return "bg-yellow-400";
    return "bg-gray-600";
}

function backupStatusBadge(status) {
    const tone = status?.error || status?.assetError
        ? "error"
        : status?.inSync && !assetsSyncing(status)
            ? "ok"
            : status?.configured || assetsSyncing(status)
                ? "warn"
                : "off";
    return span({class: "cluster-status", "data-tone": tone, "data-testid": "backup-replication-status"},
        span({class: "cluster-status-dot"}), backupStatusLabel(status));
}

// --- install command (as src/pages/cluster.js builds it) -------------------------

function primaryOpenDeployVersion() {
    const primaryID = Number(machinesS.val.find((machine) => machine.isPrimary)?.id || 0);
    if (!primaryID) return "";
    const deployment = deploymentsS.val.find((item) => placementNodeId(item.config) === primaryID && isSelfDeployment(item.config));
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
    const value = setting?.value?.value;
    if (!value) return "";
    const refID = Number(value.configRef?.configId || 0);
    const refVersion = Number(value.configRef?.version || 0);
    if (!refID || !refVersion) return (value.literal || "").trim();
    const item = (userConfigRefsS.val || []).find((ref) => Number(ref.stableId || 0) === refID && Number(ref.version || 0) === refVersion);
    return (item?.value || "").trim();
}

function dialAddress(listen) {
    const value = (listen || "").trim();
    if (!value) return "";
    const port = listenPort(value);
    if (value.startsWith(":") || value.startsWith("0.0.0.0:") || value.startsWith("[::]:")) {
        return hostPort(window.location.hostname, port);
    }
    return value;
}

function listenPort(value) {
    const bracketPort = value.match(/^\[[^\]]+\]:(\d+)$/);
    if (bracketPort) return bracketPort[1];
    const hostPortMatch = value.match(/:(\d+)$/);
    return hostPortMatch ? hostPortMatch[1] : "";
}

function hostPort(host, port) {
    if (!host || !port) return "";
    const formattedHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
    return `${formattedHost}:${port}`;
}
