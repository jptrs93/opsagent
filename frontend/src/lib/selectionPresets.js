const MAX_NAME_LENGTH = 40;
const FORMAT_VERSION = 1;

export const cleanPresetName = (name) => String(name ?? "").replace(/\s+/g, " ").trim().slice(0, MAX_NAME_LENGTH);

const sameName = (a, b) => a.toLowerCase() === b.toLowerCase();

const validId = (id) => (typeof id === "number" && Number.isFinite(id)) || (typeof id === "string" && id !== "");

const uniqueIds = (ids) => [...new Set([...(ids || [])].filter(validId))];

export function parsePresets(raw) {
    let parsed;
    try {
        parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
    } catch {
        return [];
    }
    const entries = Array.isArray(parsed) ? parsed : parsed?.presets;
    if (!Array.isArray(entries)) return [];
    const out = [];
    for (const entry of entries) {
        const name = cleanPresetName(entry?.name);
        const id = typeof entry?.id === "string" ? entry.id : "";
        if (!name || !id || !Array.isArray(entry.ids)) continue;
        if (out.some((preset) => preset.id === id || sameName(preset.name, name))) continue;
        out.push({id, name, ids: uniqueIds(entry.ids)});
    }
    return out;
}

export const serializePresets = (list) => JSON.stringify({version: FORMAT_VERSION, presets: list});

export const findPresetByName = (list, name) => {
    const clean = cleanPresetName(name);
    return clean ? list.find((preset) => sameName(preset.name, clean)) || null : null;
};

export function upsertPreset(list, name, ids, newId) {
    const clean = cleanPresetName(name);
    const members = uniqueIds(ids);
    if (!clean || members.length === 0) return null;
    const existing = findPresetByName(list, clean);
    if (existing) {
        const preset = {id: existing.id, name: clean, ids: members};
        return {preset, list: list.map((entry) => entry.id === existing.id ? preset : entry)};
    }
    const preset = {id: newId, name: clean, ids: members};
    return {preset, list: [...list, preset]};
}

export const removePreset = (list, id) => list.filter((preset) => preset.id !== id);

export const presetSelection = (preset, knownIds) => new Set(preset.ids.filter((id) => knownIds.has(id)));

export const sameSelection = (a, b) => a.size === b.size && [...a].every((id) => b.has(id));

export function matchingPreset(list, selected, knownIds) {
    if (selected.size === 0) return null;
    return list.find((preset) => sameSelection(presetSelection(preset, knownIds), selected)) || null;
}

export function matchItems(items, query) {
    const tokens = String(query ?? "").toLowerCase().split(/\s+/).filter(Boolean);
    if (tokens.length === 0) return items;
    return items.filter((item) => {
        const haystack = `${item.label ?? ""} ${item.keywords ?? ""}`.toLowerCase();
        return tokens.every((token) => haystack.includes(token));
    });
}

const defaultId = () => `p_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;

// A store keeps its own copy of the list, so saving still works for the rest
// of the visit when the storage refuses writes (private windows, quota).
export function createPresetStore({key, storage, makeId = defaultId}) {
    const read = () => {
        try {
            return parsePresets(storage?.getItem(key) ?? "[]");
        } catch {
            return [];
        }
    };
    let current = read();
    const write = (next) => {
        current = next;
        try { storage?.setItem(key, serializePresets(next)); } catch {}
    };
    return {
        list: () => [...current],
        reload: () => {
            current = read();
            return [...current];
        },
        save: (name, ids) => {
            const result = upsertPreset(current, name, ids, makeId());
            if (!result) return null;
            write(result.list);
            return result.preset;
        },
        remove: (id) => { write(removePreset(current, id)); },
    };
}
