function omitZeroValues(value) {
    if (Array.isArray(value)) {
        const items = value.map(omitZeroValues).filter(item => item !== undefined);
        return items.length ? items : undefined;
    }

    if (value instanceof Date) {
        return value.getTime() === 0 ? undefined : value;
    }

    if (value && typeof value === 'object') {
        const entries = Object.entries(value)
            .map(([key, item]) => [key, omitZeroValues(item)])
            .filter(([, item]) => item !== undefined);
        return entries.length ? Object.fromEntries(entries) : undefined;
    }

    if (value === '' || value === 0 || value === false || value === null || value === undefined) {
        return undefined;
    }

    return value;
}

function yamlKey(key) {
    return /^[A-Za-z_][A-Za-z0-9_-]*$/.test(key) ? key : JSON.stringify(key);
}

function yamlScalar(value) {
    if (value instanceof Date) return JSON.stringify(value.toISOString());
    if (typeof value === 'string') return JSON.stringify(value);
    if (typeof value === 'number' || typeof value === 'boolean') return String(value);
    return JSON.stringify(value);
}

function writeValue(value, indent) {
    if (Array.isArray(value)) {
        return value.map(item => {
            if (item && typeof item === 'object') {
                return `${indent}-\n${writeValue(item, `${indent}  `)}`;
            }
            return `${indent}- ${yamlScalar(item)}`;
        }).join('\n');
    }

    if (value && typeof value === 'object' && !(value instanceof Date)) {
        return Object.entries(value).map(([key, item]) => {
            if (Array.isArray(item) || (item && typeof item === 'object' && !(item instanceof Date))) {
                return `${indent}${yamlKey(key)}:\n${writeValue(item, `${indent}  `)}`;
            }
            return `${indent}${yamlKey(key)}: ${yamlScalar(item)}`;
        }).join('\n');
    }

    return `${indent}${yamlScalar(value)}`;
}

export function orderDeploymentConfig(config) {
    const ordered = {};
    for (const key of ['id', 'nodeId', 'identity', 'createdAt', 'version', 'updatedAt', 'updatedBy', 'spec', 'desiredState', 'deleted']) {
        if (config[key] !== undefined) ordered[key] = config[key];
    }
    for (const [key, value] of Object.entries(config)) {
        if (ordered[key] === undefined) ordered[key] = value;
    }
    return ordered;
}

export function cleanDeploymentConfig(config) {
    if (!config) return undefined;
    const cleaned = {...config};
    if (cleaned.identity) {
        cleaned.identity = {...cleaned.identity};
        delete cleaned.identity.machine;
    }
    return omitZeroValues(cleaned);
}

export function deploymentConfigToYaml(config) {
    const cleaned = cleanDeploymentConfig(config);
    if (!cleaned) return '';
    return `${writeValue(orderDeploymentConfig(cleaned), '')}\n`;
}
