// Pure helpers for the assets explorer: the column catalogue and the meta →
// row-item flattening. Tree building, filtering, and sorting are shared with
// the secrets explorer in valueExplorer.js. Kept free of van and capi imports
// so they can be unit tested outside a browser.

// Column catalogue. `fixed` columns keep their width and never absorb slack;
// everything except Name can be hidden from the column picker.
export const ASSET_COLUMNS = [
    {key: "name", label: "Name", min: 170},
    {key: "version", label: "Version", min: 80, num: true},
    {key: "created", label: "Created", min: 110},
    {key: "uses", label: "In use by", min: 90, num: true},
    {key: "size", label: "Size", min: 80, num: true},
    {key: "actions", label: "", min: 72, noSort: true, fixed: true},
];

export const ASSET_DEFAULT_COLUMNS = ["name", "version", "created", "uses", "size", "actions"];
export const ASSET_DEFAULT_COLUMN_WIDTHS = {name: 340, version: 90, created: 150, uses: 100, size: 100, actions: 72};

// Content above this size is never fetched for inline preview; the editor
// shows the size facts instead. Mirrors the backend's inline-storage threshold.
export const PREVIEW_LIMIT_BYTES = 10 * 1024 * 1024;

export const fmtSize = (n) => {
    if (!n) return "0 B";
    if (n < 1000) return `${n} B`;
    if (n < 1000 * 1000) return `${(n / 1000).toFixed(1)} KB`;
    return `${(n / 1000 / 1000).toFixed(2)} MB`;
};

// makeAssetItems flattens asset view models into the one row shape the
// explorer renders. Latest version facts come from contentVersions[0]; an
// asset with no published version is never listed by the server, but is
// skipped defensively.
export function makeAssetItems(assets) {
    return (assets || []).flatMap((meta) => {
        const latest = meta.contentVersions?.[0];
        if (!latest) return [];
        return [{
            kind: "asset",
            id: Number(meta.id),
            name: meta.key || "",
            spaceId: Number(meta.spaceId || 0),
            directoryId: Number(meta.directoryId || 0),
            version: Number(latest.version || 0),
            createdAt: latest.createdAt instanceof Date ? latest.createdAt : null,
            sizeBytes: Number(latest.sizeBytes || 0),
            large: Number(latest.sizeBytes || 0) > PREVIEW_LIMIT_BYTES,
            meta,
        }];
    });
}

// Asset directories carry `key`; the shared explorer helpers expect `name`.
export const assetDirsAsNamed = (dirs) => (dirs || []).map((d) => ({...d, name: d.key || ""}));
