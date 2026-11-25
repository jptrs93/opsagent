import {fmtSize} from "./assetExplorer.js";

export function databaseSize(status) {
    const db = Number(status?.dbSizeBytes || 0);
    const wal = Number(status?.walSizeBytes || 0);
    return {db, wal, total: db + wal};
}

export function databaseSizeText(status) {
    const {wal, total} = databaseSize(status);
    return total ? `${fmtSize(total)} (${fmtSize(wal)} WAL)` : "unknown";
}

export function databaseSizeHint(status) {
    const {db, wal, total} = databaseSize(status);
    return total ? `primary.db ${fmtSize(db)} + write-ahead log ${fmtSize(wal)}` : "";
}
