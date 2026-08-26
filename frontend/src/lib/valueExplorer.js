// Pure helpers for the secrets/configs explorer: tree building, filtering,
// sorting, and folder paths. Kept free of van and capi imports so they can be
// unit tested outside a browser.

// Column catalogue. `fixed` columns keep their width and never absorb slack;
// everything except Name can be hidden from the column picker.
export const ALL_COLUMNS = [
    {key: "name", label: "Name", min: 170},
    {key: "version", label: "Version", min: 80, num: true},
    {key: "created", label: "Created", min: 110},
    {key: "uses", label: "In use by", min: 90, num: true},
    {key: "value", label: "Value", min: 120},
    {key: "actions", label: "", min: 96, noSort: true, fixed: true},
];

export const DEFAULT_TYPES = ["secret", "config"];
export const DEFAULT_COLUMNS = ["name", "version", "created", "uses", "actions"];
export const DEFAULT_COLUMN_WIDTHS = {name: 340, version: 90, created: 150, uses: 100, value: 220, actions: 96};

export const sameSet = (a, b) => a.size === b.size && [...a].every((v) => b.has(v));

// One column absorbs the slack so the table stays flush instead of growing a
// horizontal scrollbar. Actions is fixed, so it is never the one.
export function flexColumnKey(shownColumns, columns = ALL_COLUMNS) {
    const flexible = columns.filter((c) => shownColumns.has(c.key) && !c.fixed);
    return flexible.length ? flexible[flexible.length - 1].key : "name";
}

export const itemKey = (item) => `${item.kind}:${item.id}`;

// metaVersions is the newest-first version log of a secret or config view
// model, whichever kind it is.
export const metaVersions = (meta) => meta?.valueVersions || meta?.versions || [];

// makeItems flattens secret/config view models into the one row shape the
// explorer renders. Latest version facts come from the newest log entry. When
// the secrets store is locked, secrets are left out entirely rather than shown
// unreadable.
export function makeItems(secretMetas, userConfigs, secretsUnlocked) {
    const fromMeta = (kind, meta) => {
        const latest = metaVersions(meta)[0];
        if (!latest) return [];
        return [{
            kind,
            id: Number(meta.id),
            name: meta.name || "",
            spaceId: Number(meta.spaceId || 0),
            directoryId: Number(meta.valueDirectoryId || 0),
            version: Number(latest.version || 0),
            createdAt: latest.createdAt instanceof Date ? latest.createdAt : null,
            value: kind === "config" ? String(latest.value ?? "") : "",
            meta,
        }];
    };
    return [
        ...(secretsUnlocked ? (secretMetas || []).flatMap((meta) => fromMeta("secret", meta)) : []),
        ...(userConfigs || []).flatMap((meta) => fromMeta("config", meta)),
    ];
}

export const dirsById = (dirs) => new Map((dirs || []).map((d) => [Number(d.id), d]));

// emptySpaceIds returns the spaces holding neither a folder nor an item, which
// the explorers hide by default: an empty space contributes a closed root and
// nothing else to the tree. A folder counts as content even when it holds
// nothing, so a space someone has started organising does not vanish.
//
// When every space is empty the rule is dropped and nothing is hidden: that is
// either a tree still streaming in or an install with nothing in it yet, and in
// both cases a blank page under a "0 spaces" filter reads as a fault.
export function emptySpaceIds(spaces, dirs, items) {
    const occupied = new Set();
    for (const dir of dirs || []) occupied.add(Number(dir.spaceId));
    for (const item of items || []) occupied.add(Number(item.spaceId));
    const listed = (spaces || []).map((space) => Number(space.id));
    const empty = new Set(listed.filter((id) => !occupied.has(id)));
    return empty.size === listed.length ? new Set() : empty;
}

// dirPathSegments walks a directory's ancestry to the root, returning names
// root-first. Cycle-guarded so bad data degrades to a truncated path rather
// than a hung page.
export function dirPathSegments(byId, directoryId) {
    const segments = [];
    const seen = new Set();
    let current = Number(directoryId || 0);
    while (current !== 0 && !seen.has(current)) {
        seen.add(current);
        const dir = byId.get(current);
        if (!dir) break;
        segments.unshift(dir.name || "");
        current = Number(dir.parentId || 0);
    }
    return segments;
}

export const itemPathSegments = (byId, item) => [...dirPathSegments(byId, item.directoryId), item.name];

const compareBy = (sort, usesByKey) => {
    const {key, dir} = sort;
    return (a, b) => {
        let r = 0;
        if (key === "version") r = a.version - b.version;
        else if (key === "created") r = (a.createdAt?.getTime() || 0) - (b.createdAt?.getTime() || 0);
        else if (key === "uses") r = (usesByKey.get(itemKey(a)) || 0) - (usesByKey.get(itemKey(b)) || 0);
        else if (key === "value") r = (a.value || "").localeCompare(b.value || "");
        else if (key === "size") r = (a.sizeBytes || 0) - (b.sizeBytes || 0);
        if (r === 0) r = a.name.localeCompare(b.name);
        return dir === "desc" ? -r : r;
    };
};

// buildRows produces the flat row list the table renders, walking every
// visible space's tree depth-first. Three filters compose here:
//
//   - hiddenSpaceIds hides whole roots; spaces themselves never drop out for
//     any other reason, so the two filters cannot silently hide the same row
//     from different directions.
//   - types drops non-matching items, and — while the filter is narrowed —
//     folders whose subtree has no survivors: a folder that expands to nothing
//     is worse than a folder that is not there. Unnarrowed, empty folders stay
//     visible with a count of 0 (you just created one; it has to be reachable).
//   - query matches item names and config values, and folder names. A folder
//     whose own name matches is a result in its own right: it survives on that
//     alone, even when it is empty or holds nothing else that matches, and it
//     keeps its whole subtree. An active query force-expands whatever survives,
//     since matches hidden inside collapsed folders read as none.
//
// Counts on space and folder rows follow the composed filter. hiddenByType is
// the count of items in visible spaces removed by the type filter alone.
//
// `types` is optional: a page with only one item kind (assets) passes null and
// gets no type filtering at all — the query is then the only narrowing filter.
export function buildRows({spaces, dirs, items, hiddenSpaceIds, types = null, query, expanded, sort, usesByKey = new Map()}) {
    const q = (query || "").trim().toLowerCase();
    const typeSet = types == null ? null : (types instanceof Set ? types : new Set(types));
    const narrowed = Boolean(q) || (typeSet !== null && typeSet.size < DEFAULT_TYPES.length);

    const childDirs = new Map();
    for (const d of dirs || []) {
        const k = `${Number(d.spaceId)}:${Number(d.parentId || 0)}`;
        if (!childDirs.has(k)) childDirs.set(k, []);
        childDirs.get(k).push(d);
    }
    const childItems = new Map();
    for (const item of items || []) {
        const k = `${item.spaceId}:${item.directoryId}`;
        if (!childItems.has(k)) childItems.set(k, []);
        childItems.get(k).push(item);
    }

    const cmpItems = compareBy(sort, usesByKey);
    const dirSign = sort.key === "name" && sort.dir === "desc" ? -1 : 1;
    const cmpDirs = (a, b) => dirSign * (a.name || "").localeCompare(b.name || "");

    const matchesQuery = (item) => !q ||
        item.name.toLowerCase().includes(q) ||
        (item.kind === "config" && item.value.toLowerCase().includes(q));

    const visited = new Set();
    // `kept` reports whether the subtree produced any row at all, which is not
    // the same as `count > 0`: a folder retained purely because its own name
    // matches counts zero items but must still hold its ancestors open.
    const walk = (spaceId, parentId, depth, ancestorMatch) => {
        const key = `${spaceId}:${parentId}`;
        if (visited.has(key)) return {count: 0, rows: [], kept: false};
        visited.add(key);
        const rows = [];
        let count = 0;
        let kept = false;
        for (const dir of [...(childDirs.get(key) || [])].sort(cmpDirs)) {
            const selfMatch = q !== "" && (dir.name || "").toLowerCase().includes(q);
            const sub = walk(spaceId, Number(dir.id), depth + 1, ancestorMatch || selfMatch);
            if (narrowed && !selfMatch && !sub.kept) continue;
            count += sub.count;
            kept = true;
            const dirKey = `dir:${dir.id}`;
            const open = q ? true : expanded.has(dirKey);
            rows.push({type: "dir", dir, key: dirKey, depth, count: sub.count, expanded: open});
            if (open) rows.push(...sub.rows);
        }
        for (const item of [...(childItems.get(key) || [])].sort(cmpItems)) {
            if (typeSet !== null && !typeSet.has(item.kind)) continue;
            if (!ancestorMatch && !matchesQuery(item)) continue;
            count += 1;
            kept = true;
            rows.push({type: "item", item, key: itemKey(item), depth});
        }
        return {count, rows, kept};
    };

    const rows = [];
    let hiddenByType = 0;
    for (const item of items || []) {
        if (typeSet !== null && !hiddenSpaceIds.has(item.spaceId) && !typeSet.has(item.kind)) hiddenByType += 1;
    }
    for (const space of spaces || []) {
        const spaceId = Number(space.id);
        if (hiddenSpaceIds.has(spaceId)) continue;
        const sub = walk(spaceId, 0, 1, false);
        const spaceKey = `space:${spaceId}`;
        const open = q ? sub.kept : expanded.has(spaceKey);
        rows.push({type: "space", space, key: spaceKey, depth: 0, count: sub.count, expanded: open});
        if (open) rows.push(...sub.rows);
    }
    return {rows, hiddenByType};
}

// descendantDirIds collects a directory's whole subtree, itself included —
// the set a folder move must exclude as destinations.
export function descendantDirIds(dirs, rootId) {
    const children = new Map();
    for (const d of dirs || []) {
        const parent = Number(d.parentId || 0);
        if (!children.has(parent)) children.set(parent, []);
        children.get(parent).push(Number(d.id));
    }
    const out = new Set();
    const queue = [Number(rootId)];
    while (queue.length) {
        const id = queue.pop();
        if (out.has(id)) continue;
        out.add(id);
        queue.push(...(children.get(id) || []));
    }
    return out;
}

// folderOptions lists move/create destinations in one space: the root first,
// then every folder by its full path. excludeSubtreeOf (a directory id) drops
// a moved folder's own subtree from the candidates.
export function folderOptions(dirs, spaceId, excludeSubtreeOf = 0) {
    const excluded = excludeSubtreeOf ? descendantDirIds(dirs, excludeSubtreeOf) : new Set();
    const byId = dirsById(dirs);
    return [
        {id: 0, label: "/"},
        ...(dirs || [])
            .filter((d) => Number(d.spaceId) === Number(spaceId) && !excluded.has(Number(d.id)))
            .map((d) => ({id: Number(d.id), label: dirPathSegments(byId, d.id).join("/")}))
            .sort((a, b) => a.label.localeCompare(b.label)),
    ];
}

// dropDestination maps the row under the cursor to the folder a drop would land
// in. Dropping on a folder means "into it"; dropping on an item means "beside
// it", i.e. into its parent; dropping on a space means that space's root.
export function dropDestination(row) {
    if (!row) return null;
    if (row.type === "space") return {spaceId: Number(row.space.id), directoryId: 0};
    if (row.type === "dir") return {spaceId: Number(row.dir.spaceId), directoryId: Number(row.dir.id)};
    if (row.type === "item") return {spaceId: Number(row.item.spaceId), directoryId: Number(row.item.directoryId)};
    return null;
}

// dragSource describes the row being dragged, flattened so items and folders
// can be checked by one set of rules. key is the row key used for selection.
export function dragSource(row) {
    if (row?.type === "dir") {
        return {
            type: "dir", key: row.key, id: Number(row.dir.id), name: row.dir.name,
            spaceId: Number(row.dir.spaceId), parentId: Number(row.dir.parentId || 0),
        };
    }
    if (row?.type === "item") {
        // kind ("asset" | "secret" | "config") picks the move endpoint; the drop
        // rules themselves treat every item the same.
        return {
            type: "item", kind: row.item.kind, key: row.key, id: Number(row.item.id), name: row.item.name,
            spaceId: Number(row.item.spaceId), parentId: Number(row.item.directoryId || 0),
        };
    }
    return null;
}

// checkDrop decides whether a drag may land on a destination, and why not when
// it may not.
//
// Cross-space drops are deliberately allowed through: the server owns that
// answer (it currently refuses), and letting the drop happen surfaces its error
// instead of silently doing nothing under the cursor. Everything a client can
// know for certain — a folder swallowing itself, a name already taken, a move
// that changes nothing — is settled here so the drag shows it live.
//
// Returns {ok, reason, crossSpace}. reason is "" when ok, or when the drop is a
// no-op that needs no explanation.
export function checkDrop({dirs, items, drag, destination}) {
    if (!drag || !destination) return {ok: false, reason: "", crossSpace: false};
    const crossSpace = destination.spaceId !== drag.spaceId;

    if (drag.type === "dir") {
        // Itself, or anywhere inside its own subtree: the subtree would be cut
        // loose from the tree entirely.
        if (descendantDirIds(dirs, drag.id).has(destination.directoryId)) {
            return {ok: false, reason: "A folder cannot be moved inside itself.", crossSpace};
        }
    }
    // Left to the server, which knows whether cross-space moves are supported
    // yet. Checking names across the boundary would report the wrong reason.
    if (crossSpace) return {ok: true, reason: "", crossSpace};

    if (destination.directoryId === drag.parentId) return {ok: false, reason: "", crossSpace};

    const taken = (dirs || []).some((d) => Number(d.spaceId) === destination.spaceId
            && Number(d.parentId || 0) === destination.directoryId
            && d.name === drag.name
            && !(drag.type === "dir" && Number(d.id) === drag.id))
        || (items || []).some((it) => Number(it.spaceId) === destination.spaceId
            && Number(it.directoryId || 0) === destination.directoryId
            && it.name === drag.name
            && !(drag.type === "item" && Number(it.id) === drag.id));
    if (taken) return {ok: false, reason: `"${drag.name}" already exists there.`, crossSpace};

    return {ok: true, reason: "", crossSpace};
}

// Deterministic space accents: the opendeploy space is fixed slate, user
// spaces cycle a small palette by id so the dot stays stable across sessions.
const SPACE_HUES = ["#f59e0b", "#2dd4bf", "#a78bfa", "#f472b6", "#4ade80", "#60a5fa", "#fb923c", "#94a3b8"];

export function spaceHue(spaceId) {
    const id = Number(spaceId);
    if (id === 0) return "#64748b";
    return SPACE_HUES[(id - 1) % SPACE_HUES.length];
}
