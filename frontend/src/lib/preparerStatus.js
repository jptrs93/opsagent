// Preparer status values and the shared phrasing for them.
//
// The model generator emits typedefs and wire codecs but no enum constants, so
// these mirror the enums in api-contract/model/scheduled_instances.proto by hand and must
// be kept in step with it.
//
// Preparation runs in two stages: resolving runtime inputs (assets, secrets,
// configs), then producing the image. PreparerStatus carries only those two —
// the rollup that the backend gates runner start on is derived from the pair by
// rollupOf() below, mirroring apigen.PreparerStatus.Rollup(). Keep the two in
// step.

export const PreparationStatus = {
    UNKNOWN: 0,
    PREPARING: 2,
    DOWNLOADING: 3,
    READY: 4,
    FAILED: 5,
    PULLING: 6,
};

export const InputsStatus = {
    UNKNOWN: 0,
    RESOLVING: 1,
    READY: 2,
    FAILED: 3,
};

export const ImageStatus = {
    UNKNOWN: 0,
    BUILDING: 1,
    PULLING: 2,
    DOWNLOADING: 3,
    READY: 4,
    FAILED: 5,
};

const rollupLabels = {
    [PreparationStatus.UNKNOWN]: 'unknown',
    [PreparationStatus.PREPARING]: 'preparing',
    [PreparationStatus.DOWNLOADING]: 'downloading',
    [PreparationStatus.READY]: 'ready',
    [PreparationStatus.FAILED]: 'failed',
    [PreparationStatus.PULLING]: 'pulling',
};

const inputsLabels = {
    [InputsStatus.UNKNOWN]: 'unknown',
    [InputsStatus.RESOLVING]: 'resolving',
    [InputsStatus.READY]: 'ready',
    [InputsStatus.FAILED]: 'failed',
};

const imageLabels = {
    [ImageStatus.UNKNOWN]: 'unknown',
    [ImageStatus.BUILDING]: 'building',
    [ImageStatus.PULLING]: 'pulling',
    [ImageStatus.DOWNLOADING]: 'downloading',
    [ImageStatus.READY]: 'ready',
    [ImageStatus.FAILED]: 'failed',
};

const label = (table, value) => table[value] ?? `${value}`;

export const rollupLabel = (status) => label(rollupLabels, status || 0);
export const inputsLabel = (status) => label(inputsLabels, status || 0);
export const imageLabel = (status) => label(imageLabels, status || 0);

export function isPrepareInProgress(status) {
    return status === PreparationStatus.PREPARING
        || status === PreparationStatus.DOWNLOADING
        || status === PreparationStatus.PULLING;
}

// rollupOf derives the single status the backend gates on. Mirrors
// apigen.PreparerStatus.Rollup() — a ready image outranks a resolving inputs
// stage, so an input retry on an already-prepared instance stays READY.
export function rollupOf(preparer) {
    const p = preparer || {};
    const inputs = p.inputs || 0;
    const image = p.image || 0;

    if (inputs === InputsStatus.FAILED || image === ImageStatus.FAILED) return PreparationStatus.FAILED;
    switch (image) {
        case ImageStatus.READY: return PreparationStatus.READY;
        case ImageStatus.PULLING: return PreparationStatus.PULLING;
        case ImageStatus.DOWNLOADING: return PreparationStatus.DOWNLOADING;
        case ImageStatus.BUILDING: return PreparationStatus.PREPARING;
    }
    if (inputs !== InputsStatus.UNKNOWN) return PreparationStatus.PREPARING;
    return PreparationStatus.UNKNOWN;
}

// preparerPhase reduces a PreparerStatus to the one phrase worth showing, with a
// tone of 'progress' | 'ready' | 'failed'. Returns null when nothing has been
// recorded yet.
export function preparerPhase(preparer) {
    const p = preparer || {};
    const status = rollupOf(p);
    const inputs = p.inputs || 0;
    const image = p.image || 0;

    if (status === PreparationStatus.FAILED) {
        if (inputs === InputsStatus.FAILED) return {tone: 'failed', label: 'inputs failed'};
        if (image === ImageStatus.FAILED) return {tone: 'failed', label: 'image failed'};
        return {tone: 'failed', label: 'failed'};
    }
    if (status === PreparationStatus.READY) {
        // An input retry on an already-prepared instance leaves the rollup READY
        // on purpose, so the runner keeps running. Surface the retry anyway —
        // being invisible is what made this state hard to diagnose. The Status
        // badge on the same line still reads Running, so the label need not
        // repeat that it is ready.
        if (inputs === InputsStatus.RESOLVING) return {tone: 'progress', label: 'retrying inputs'};
        return {tone: 'ready', label: 'ready'};
    }
    if (inputs === InputsStatus.RESOLVING) return {tone: 'progress', label: 'resolving inputs'};
    switch (image) {
        case ImageStatus.BUILDING: return {tone: 'progress', label: 'building image'};
        case ImageStatus.PULLING: return {tone: 'progress', label: 'pulling image'};
        case ImageStatus.DOWNLOADING: return {tone: 'progress', label: 'downloading'};
    }
    if (isPrepareInProgress(status)) return {tone: 'progress', label: 'in progress'};
    return null;
}
