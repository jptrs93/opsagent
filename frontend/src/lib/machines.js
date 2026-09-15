export function nodeDisplayName(nodeId, machines) {
    const id = Number(nodeId || 0);
    if (!id) return '-';
    const machine = (machines || []).find(machine => Number(machine.id) === id);
    if (!machine) return `node ${id}`;
    return machine.evicted ? `${machine.name || `node ${id}`} (evicted)` : machine.name || `node ${id}`;
}
