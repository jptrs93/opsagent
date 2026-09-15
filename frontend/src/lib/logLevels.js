// Log level buckets as the query histogram reports them, in stacking order
// top→bottom: the four named levels, OTHER for any other parsed level (TRACE,
// FATAL, ...) and '' for lines with no parsed level.
export const LOG_NAMED_LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG'];
export const LOG_LEVEL_OTHER = 'OTHER';
export const LOG_LEVEL_NONE = '';
export const LOG_LEVELS = [...LOG_NAMED_LEVELS, LOG_LEVEL_OTHER, LOG_LEVEL_NONE];

// The four named fills were validated for CVD separation and contrast on this
// surface (dataviz six-check validator); OTHER's violet sits between the teal
// and gray it stacks against. Row/legend text stays on text tokens.
const META = {
    ERROR: {fill: '#c42121', text: 'text-red-400', label: 'ERROR'},
    WARN: {fill: '#c67b04', text: 'text-amber-400', label: 'WARN'},
    INFO: {fill: '#3b82f6', text: 'text-blue-400', label: 'INFO'},
    DEBUG: {fill: '#0e9488', text: 'text-teal-500', label: 'DEBUG'},
    [LOG_LEVEL_OTHER]: {fill: '#7c3aed', text: 'text-violet-400', label: 'OTHER'},
    [LOG_LEVEL_NONE]: {fill: '#6b7280', text: 'text-gray-400', label: 'NONE'},
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
