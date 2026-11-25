export const MIN_COLUMN_WIDTH = 28;
export const MAX_COLUMN_WIDTH = 800;

export const clampWidth = (px, min = MIN_COLUMN_WIDTH, max = MAX_COLUMN_WIDTH) =>
    Math.round(Math.min(max, Math.max(min, px)));

export function readColumnWidths(raw, columns) {
    let saved;
    try {
        saved = JSON.parse(raw || "{}");
    } catch {
        return {};
    }
    if (!saved || typeof saved !== "object" || Array.isArray(saved)) return {};
    const widths = {};
    for (const column of columns) {
        const px = saved[column.key];
        if (column.flex || typeof px !== "number" || !Number.isFinite(px) || px <= 0) continue;
        widths[column.key] = clampWidth(px, column.min, column.max);
    }
    return widths;
}

export const withoutColumn = (widths, key) =>
    Object.fromEntries(Object.entries(widths).filter(([k]) => k !== key));

export const columnWidthVars = (widths, prefix) =>
    Object.entries(widths).map(([key, px]) => `--${prefix}-${key}:${px}px`).join(";");
