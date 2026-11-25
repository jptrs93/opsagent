const num = (v) => v == null ? NaN : Number(v);

export const DISK_WARN_PCT = 80;
export const DISK_FULL_PCT = 90;

export function diskUsage(fs) {
    if (!fs) return null;
    const total = num(fs.totalBytes);
    const free = num(fs.freeBytes);
    if (!(total > 0) || !Number.isFinite(free)) return null;
    const used = Math.max(0, total - free);
    const avail = Number.isFinite(num(fs.availBytes)) ? num(fs.availBytes) : free;
    const usable = used + avail;
    return {total, used, avail, usedPct: usable > 0 ? used / usable * 100 : 100};
}

export const dataDiskUsage = (sample) => sample ? diskUsage({
    totalBytes: sample.fsDataTotalBytes,
    freeBytes: sample.fsDataFreeBytes,
    availBytes: sample.fsDataAvailBytes,
}) : null;

export function extraDiskUsages(sample) {
    const out = [];
    for (const [mount, fs] of Object.entries(sample?.extraFilesystems || {})) {
        const usage = diskUsage(fs);
        if (mount && usage) out.push({mount, ...usage});
    }
    return out.sort((a, b) => a.mount.localeCompare(b.mount));
}

export const usageTone = (pct) => !Number.isFinite(pct) ? '' : pct >= DISK_FULL_PCT ? 'error' : pct >= DISK_WARN_PCT ? 'warn' : '';

export function latestByNode(resp) {
    const out = new Map();
    for (const entry of resp?.entries || []) {
        const id = Number(entry.sample?.nodeId || 0);
        if (id) out.set(id, entry);
    }
    return out;
}

const rateOf = (entry, field) => {
    const r = (entry.rates || []).find(x => x.field === field);
    return r ? Number(r.perSecond) : NaN;
};

export function nodeOverviewRow(entry, fetchedAt) {
    const s = entry?.sample;
    if (!s) {
        return {cpu: NaN, cpuCount: NaN, cpuPct: NaN, load: NaN, memUsed: NaN, memTotal: NaN, memPct: NaN, disk: null, diskUsed: NaN, diskPct: NaN, rx: NaN, tx: NaN, read: NaN, write: NaN, age: NaN};
    }
    const cpu = rateOf(entry, 'cpu_usage_usec') / 1e6;
    const cpuCount = num(s.cpuCount);
    const memTotal = num(s.memTotal);
    const memUsed = memTotal - num(s.memAvailable);
    const disk = dataDiskUsage(s);
    return {
        cpu,
        cpuCount,
        cpuPct: cpuCount > 0 ? cpu / cpuCount * 100 : NaN,
        load: num(s.load1),
        memUsed,
        memTotal,
        memPct: memTotal > 0 ? memUsed / memTotal * 100 : NaN,
        disk,
        diskUsed: disk ? disk.used : NaN,
        diskPct: disk ? disk.usedPct : NaN,
        rx: rateOf(entry, 'net_rx_bytes'),
        tx: rateOf(entry, 'net_tx_bytes'),
        read: rateOf(entry, 'io_read_bytes'),
        write: rateOf(entry, 'io_write_bytes'),
        age: Math.max(0, Math.round((fetchedAt - Number(s.time)) / 1000)),
    };
}
