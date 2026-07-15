export function nodeDisplayName(nodeId, machines) {
    const id = Number(nodeId || 0);
    if (!id) return '-';
    return (machines || []).find(machine => Number(machine.id) === id)?.name || `node ${id}`;
}
