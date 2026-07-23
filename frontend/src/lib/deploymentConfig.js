export function containerWorkload(config) {
    return config?.spec?.container1Spec || null;
}

export function deploymentWorkload(config) {
    return containerWorkload(config) || config?.spec?.systemdSpec || null;
}
