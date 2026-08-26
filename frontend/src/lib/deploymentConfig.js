export function containerWorkload(config) {
    return config?.spec?.container1Spec || null;
}

export function deploymentWorkload(config) {
    const container = containerWorkload(config);
    if (container) return container;
    const opendeploy = config?.spec?.opendeploySpec;
    if (opendeploy) return {...opendeploy, running: true};
    return null;
}
