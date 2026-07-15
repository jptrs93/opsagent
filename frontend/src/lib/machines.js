export function machineDisplayName(identifier, machines) {
    if (!identifier) return '-';
    return (machines || []).find(machine => machine.identifier === identifier)?.name || '';
}
