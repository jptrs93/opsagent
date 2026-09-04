// Pure helpers for the node/space allow list. Kept free of van and capi imports
// so they can be unit tested outside a browser.

// The opendeploy space is an invariant of the allow list: the server unions it
// back in on every read and write, so the UI renders it as fixed rather than
// offering a checkbox that cannot be unticked.
export const OPENDEPLOY_SPACE_ID = 0;

export const isFixedSpace = (spaceID) => Number(spaceID) === OPENDEPLOY_SPACE_ID;

// Spaces a person can browse, filter on, or create into. The opendeploy space
// holds the tool's own internal secrets, configs and assets, which are managed
// by the installer and settings pages rather than the explorers, so it is left
// out of every space list rather than offered and then filtered away.
export function selectableSpaces(spaces) {
    return (spaces || []).filter((space) => !isFixedSpace(space.id));
}

export function nodeAllowsSpace(node, spaceID) {
    return (node?.allowedSpaces || []).some((id) => Number(id) === Number(spaceID));
}

// Nodes that can host the given space, in the order they were given.
export function nodesForSpace(nodes, spaceID) {
    return (nodes || []).filter((node) => nodeAllowsSpace(node, spaceID));
}

// Space ids a node allows, minus the fixed one, for editing. The fixed space is
// added back by the server, so leaving it out of the editable set keeps the UI
// from implying it is a choice.
export function editableSpaceIDs(node) {
    return (node?.allowedSpaces || [])
        .map(Number)
        .filter((id) => !isFixedSpace(id));
}

// Renders an allow list for display. Named spaces first in the order given,
// unknown ids last as raw numbers so a stale id is visible rather than dropped.
export function allowedSpaceNames(node, spaces) {
    const byID = new Map((spaces || []).map((space) => [Number(space.id), space.name]));
    return (node?.allowedSpaces || [])
        .map(Number)
        .map((id) => byID.get(id) || `space ${id}`);
}

// Default placement for a new deployment: the first (space, node) pair the
// person can see that the allow list permits. The preferred space (global by
// default) wins when any node hosts it; otherwise selectable spaces are tried
// in order. Null when no node hosts any visible space, which leaves the form
// on the global space with no node.
export function defaultPlacement(spaces, nodes, preferredSpaceID = 1) {
    const candidates = selectableSpaces(spaces).filter((space) => !space?.deleted);
    const hosts = (nodes || []).filter((node) => Number(node?.id || 0));
    const ordered = [
        ...candidates.filter((space) => Number(space.id) === Number(preferredSpaceID)),
        ...candidates.filter((space) => Number(space.id) !== Number(preferredSpaceID)),
    ];
    for (const space of ordered) {
        const node = hosts.find((item) => nodeAllowsSpace(item, space.id));
        if (node) return {spaceId: Number(space.id), nodeId: Number(node.id)};
    }
    return null;
}

// Placeholder name for a new deployment: deployment-<6 random [a-z0-9]>.
// Names must be unique per node and space, so a random suffix keeps two
// unedited creates from colliding.
export function placeholderDeploymentName(random = Math.random) {
    const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
    let suffix = "";
    for (let i = 0; i < 6; i++) suffix += alphabet[Math.floor(random() * alphabet.length) % alphabet.length];
    return `deployment-${suffix}`;
}
