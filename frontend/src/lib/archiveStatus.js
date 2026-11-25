import {fmtSize} from "./assetExplorer.js";

const KINDS = [["logs", "Logs"], ["deploymentMetrics", "Deployment metrics"], ["nodeMetrics", "Node metrics"]];

const counts = (c) => ({chunks: Number(c?.chunks || 0), bytes: Number(c?.bytes || 0)});
const add = (a, b) => ({chunks: a.chunks + b.chunks, bytes: a.bytes + b.bytes});

// A record is archived, or held only by its node (pending), or both
// archived and still on its node. The files a node has recorded are
// therefore the archived ones plus the pending ones.
export function kindSummary(kind) {
    const local = counts(kind?.local), archived = counts(kind?.archived), pending = counts(kind?.pending);
    return {local, archived, pending, total: add(archived, pending), lastArchivedAt: Number(kind?.lastArchivedAt || 0)};
}

export function kindText(summary) {
    return summary.total.chunks ? `${summary.archived.chunks} / ${summary.total.chunks}` : "-";
}

export function kindHint(summary) {
    if (!summary.total.chunks) return "No sealed files recorded";
    return `${fmtSize(summary.archived.bytes)} archived, ${fmtSize(summary.local.bytes)} on the node, ${fmtSize(summary.total.bytes)} recorded`;
}

export function pendingText(pending) {
    if (!pending.chunks) return "-";
    return `${pending.chunks} ${pending.chunks === 1 ? "file" : "files"} · ${fmtSize(pending.bytes)}`;
}

export function archiveRows(status, machines = []) {
    const byId = new Map(machines.map((m) => [Number(m.id), m]));
    return (status?.nodes || []).map((node) => {
        const id = Number(node.nodeId || 0);
        const machine = byId.get(id);
        const kinds = KINDS.map(([key, label]) => ({key, label, ...kindSummary(node[key])}));
        return {
            id,
            name: machine?.name || machine?.identifier || `node ${id}`,
            evicted: Boolean(machine?.evicted),
            kinds,
            pending: kinds.reduce((sum, k) => add(sum, k.pending), {chunks: 0, bytes: 0}),
            archived: kinds.reduce((sum, k) => add(sum, k.archived), {chunks: 0, bytes: 0}),
            lastArchivedAt: Math.max(0, ...kinds.map((k) => k.lastArchivedAt)),
        };
    }).sort((a, b) => a.id - b.id);
}

export function archiveTotals(rows) {
    return rows.reduce((sum, row) => ({pending: add(sum.pending, row.pending), archived: add(sum.archived, row.archived)}),
        {pending: {chunks: 0, bytes: 0}, archived: {chunks: 0, bytes: 0}});
}

export function archiveTone(status, rows) {
    if (!status?.enabled) return "off";
    return archiveTotals(rows).pending.chunks ? "warn" : "ok";
}

export function archiveLabel(status, rows) {
    if (!status?.enabled) return "off";
    const {pending, archived} = archiveTotals(rows);
    if (pending.chunks) return `${pending.chunks} waiting`;
    return archived.chunks ? "archived" : "on";
}

export function archiveSentence(status, rows) {
    if (!status?.enabled) {
        return "Sealed log and metrics files stay on the node that wrote them. Turn on \"Archive logs and metrics\" under Settings, Backup, to copy them to the backup bucket.";
    }
    const {pending, archived} = archiveTotals(rows);
    if (pending.chunks) return `${pendingText(pending)} still to send. Each node sends its sealed files to the backup bucket once an hour.`;
    if (!archived.chunks) return "Nothing to send yet. A log or metrics file is sealed after its day ends.";
    return "Every sealed log and metrics file is in the backup bucket.";
}
