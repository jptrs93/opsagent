export const envPrefix = key => {
    const trimmed = (key || '').trim();
    const index = trimmed.indexOf('_');
    return index > 0 ? trimmed.slice(0, index) : trimmed;
};

export function groupEnvRows(rows) {
    const byPrefix = new Map();
    for (const row of rows || []) {
        const prefix = envPrefix(row.key);
        if (!byPrefix.has(prefix)) byPrefix.set(prefix, []);
        byPrefix.get(prefix).push(row);
    }
    const groups = [];
    const noGroup = [];
    for (const [prefix, members] of byPrefix) {
        if (prefix && members.length > 1) groups.push({prefix, rows: members});
        else noGroup.push(...members);
    }
    groups.sort((a, b) => a.prefix.localeCompare(b.prefix));
    if (noGroup.length) groups.push({prefix: '', rows: noGroup});
    return groups;
}

export const isBooleanRow = row =>
    (row.type || 'value') === 'value' && (row.key || '').trim().toUpperCase().endsWith('ENABLED');

export const isTruthyEnvValue = value =>
    ['true', '1', 'yes', 'on'].includes(String(value ?? '').trim().toLowerCase());
