// Log level buckets as the query histogram reports them, in stacking order
// top→bottom: the four named levels, OTHER for any other parsed level (TRACE,
// FATAL, ...) and '' for lines with no parsed level.
export const LOG_NAMED_LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG'];
export const LOG_LEVEL_OTHER = 'OTHER';
export const LOG_LEVEL_NONE = '';
export const LOG_LEVELS = [...LOG_NAMED_LEVELS, LOG_LEVEL_OTHER, LOG_LEVEL_NONE];

// The colours are per theme in styles/logs.css: fill is a CSS colour for a
// style attribute (never an SVG presentation attribute, which ignores var()),
// text a class.
const META = {
    ERROR: {fill: 'var(--logs-fill-error)', text: 'logs-lvl-error', label: 'ERROR'},
    WARN: {fill: 'var(--logs-fill-warn)', text: 'logs-lvl-warn', label: 'WARN'},
    INFO: {fill: 'var(--logs-fill-info)', text: 'logs-lvl-info', label: 'INFO'},
    DEBUG: {fill: 'var(--logs-fill-debug)', text: 'logs-lvl-debug', label: 'DEBUG'},
    [LOG_LEVEL_OTHER]: {fill: 'var(--logs-fill-other)', text: 'logs-lvl-other', label: 'OTHER'},
    [LOG_LEVEL_NONE]: {fill: 'var(--logs-fill-none)', text: 'logs-lvl-none', label: 'NONE'},
};

// logLevelBucket maps a record's parsed level onto its histogram bucket.
export const logLevelBucket = (level) => LOG_NAMED_LEVELS.includes(level) ? level : (level ? LOG_LEVEL_OTHER : LOG_LEVEL_NONE);
export const logLevelMeta = (level) => META[logLevelBucket(level)];

// logLevelFilters expresses a level selection ({level: bool} over LOG_LEVELS)
// as wire filters; an all-on selection needs none. Filters AND together and
// there is no not-in op, so a selection that includes OTHER is written as
// exclusions: one neq per deselected named level, plus exists when NONE is
// off. Any other selection is one in over the selected levels, where ''
// matches lines with no level.
export function logLevelFilters(on) {
    if (LOG_LEVELS.every(l => on[l])) return [];
    if (on[LOG_LEVEL_OTHER]) {
        const filters = LOG_NAMED_LEVELS.filter(l => !on[l]).map(l => ({field: 'level', op: 'neq', value: l}));
        if (!on[LOG_LEVEL_NONE]) filters.push({field: 'level', op: 'exists'});
        return filters;
    }
    const values = LOG_NAMED_LEVELS.filter(l => on[l]);
    if (on[LOG_LEVEL_NONE]) values.push(LOG_LEVEL_NONE);
    return [{field: 'level', op: 'in', values}];
}
