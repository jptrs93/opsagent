import {referenceCatalogForDocument} from "./mockStateStream.js";

const allowedChildren = {
    deployment: new Set(["identity", "container", "network"]),
    container: new Set(["source", "process", "env_vars", "resources", "upgrade"]),
    source: new Set(["container_image", "nix_docker_build"]),
    env_vars: new Set(),
    network: new Set(),
};

export const schemaSections = [
    {
        name: "Deployment",
        signature: "deployment { node = node(\"name\") ... }",
        description: "One root block. Symbolic space and node names are resolved to placement IDs on save.",
        fields: ["node = node(\"name\")", "identity", "name", "space = space(\"name\")"],
    },
    {
        name: "Container",
        signature: "container { source { ... } }",
        description: "A repeatable workload boundary containing its source, process, environment, mounts, resources, upgrade policy, and artifact version.",
        fields: ["source", "process", "env_vars", "mounts = [...]", "resources", "upgrade", "version"],
    },
    {
        name: "Named references",
        signature: "space(\"name\") | asset(\"name\"[, { version = number }])",
        description: "Every symbolic resource uses a typed function with a quoted name. Versioned resources default to latest.",
        fields: ["space(\"name\")", "node(\"name\")", "secret(\"name\"[, { version = number }])", "config(\"name\"[, { version = number }])", "asset(\"name\"[, { version = number }])", "deployment(\"space\", \"deployment\")", "address(\"space\", \"deployment\")"],
    },
    {
        name: "Networking",
        signature: "network { mode = \"virtual\" }",
        description: "Deployment networking. Ingress routes are function expressions in a list.",
        fields: ["mode", "ingress = [...]", "port_forward(...)", "tls_passthrough(...)"],
    },
    {
        name: "Desired state",
        signature: "desired_running = true",
        description: "Whether the deployment should be running.",
        fields: ["true", "false"],
    },
];

export const completionOptions = [
    {label: "deployment", type: "keyword", info: "Root deployment block"},
    {label: "identity", type: "keyword", info: "Deployment name and space"},
    {label: "container", type: "keyword", info: "Repeatable workload container"},
    {label: "source", type: "keyword", info: "Artifact source block"},
    {label: "container_image", type: "keyword", info: "Existing container image source"},
    {label: "nix_docker_build", type: "keyword", info: "Nix-built container image source"},
    {label: "network", type: "keyword", info: "Network mode block"},
    {label: "process", type: "keyword", info: "Process settings section"},
    {label: "env_vars", type: "keyword", info: "Environment variables section"},
    {label: "mount", type: "function", info: "Mount a typed source at a container path"},
    {label: "default_volume", type: "function", info: "Reference this deployment's default volume"},
    {label: "resources", type: "keyword", info: "Resource overrides section"},
    {label: "upgrade", type: "keyword", info: "Upgrade behavior section"},
    {label: "secret", type: "function", info: "Reference a secret by name and optional version"},
    {label: "config", type: "function", info: "Reference a config by name and optional version"},
    {label: "asset", type: "function", info: "Reference an asset by name and optional version"},
    {label: "deployment", type: "function", info: "Reference a deployment by space and stable name"},
    {label: "space", type: "function", info: "Reference a space by stable name"},
    {label: "node", type: "function", info: "Reference a cluster node by stable name"},
    {label: "address", type: "function", info: "Reference a deployment address by space and stable name"},
    {label: "port_forward", type: "function", info: "Forward a TCP or UDP host port"},
    {label: "tls_passthrough", type: "function", info: "Route TLS by SNI without termination"},
    ...[
        "name", "image", "repo", "flake", "target", "user", "command", "mode", "mounts",
        "working_dir", "strategy",
        "readiness_timeout_seconds", "dev_shm_size_kb", "file_descriptor_limit", "value",
        "read_only", "executable", "hostname", "host_port", "container_port", "version", "ingress", "desired_running",
    ].map(label => ({label, type: "property"})),
];

function skipQuoted(text, index) {
    const quote = text[index++];
    while (index < text.length) {
        if (text[index] === "\\") {
            index += 2;
            continue;
        }
        if (text[index++] === quote) break;
    }
    return index;
}

function skipBlockHeaderSpace(text, start) {
    let index = start;
    while (index < text.length) {
        if (text[index] === " " || text[index] === "\t") {
            index++;
            continue;
        }
        if (text[index] === "/" && text[index + 1] === "*") {
            const end = text.indexOf("*/", index + 2);
            if (end < 0 || text.slice(index, end).includes("\n")) return index;
            index = end + 2;
            continue;
        }
        break;
    }
    return index;
}

export function scanBlocks(text) {
    const blocks = [];
    const braces = [];
    let index = 0;

    while (index < text.length) {
        if (text[index] === '"' || text[index] === "'") {
            index = skipQuoted(text, index);
            continue;
        }
        if (text[index] === "#" || (text[index] === "/" && text[index + 1] === "/")) {
            index = text.indexOf("\n", index);
            if (index < 0) break;
            continue;
        }
        if (text[index] === "/" && text[index + 1] === "*") {
            const end = text.indexOf("*/", index + 2);
            index = end < 0 ? text.length : end + 2;
            continue;
        }
        if (text[index] === "}") {
            const entry = braces.pop();
            if (entry) entry.end = index + 1;
            index++;
            continue;
        }
        if (text[index] === "{") {
            braces.push(null);
            index++;
            continue;
        }
        if (!/[A-Za-z_]/.test(text[index])) {
            index++;
            continue;
        }

        const start = index;
        while (/[A-Za-z0-9_-]/.test(text[index] || "")) index++;
        const type = text.slice(start, index);
        let cursor = skipBlockHeaderSpace(text, index);
        const labels = [];
        while (text[cursor] === '"' || /[A-Za-z_]/.test(text[cursor] || "")) {
            if (text[cursor] === '"') {
                const labelStart = cursor;
                cursor = skipQuoted(text, cursor);
                labels.push(text.slice(labelStart + 1, cursor - 1));
            } else {
                const labelStart = cursor;
                while (/[A-Za-z0-9_-]/.test(text[cursor] || "")) cursor++;
                labels.push(text.slice(labelStart, cursor));
            }
            cursor = skipBlockHeaderSpace(text, cursor);
        }
        if (text[cursor] !== "{") continue;

        const parent = [...braces].reverse().find(Boolean) || null;
        const block = {type, labels, from: start, open: cursor, end: text.length, parent};
        blocks.push(block);
        braces.push(block);
        index = cursor + 1;
    }

    return {blocks, unmatched: braces.length};
}

function blockChildren(blocks, parent, type) {
    return blocks.filter(block => block.parent === parent && (!type || block.type === type));
}

function blockBody(text, block) {
    return text.slice(block.open + 1, Math.max(block.open + 1, block.end - 1));
}

function removeInlineComment(value) {
    let quoted = false;
    for (let index = 0; index < value.length; index++) {
        if (value[index] === "\\") {
            index++;
            continue;
        }
        if (value[index] === '"') quoted = !quoted;
        if (!quoted && (value[index] === "#" || (value[index] === "/" && value[index + 1] === "/"))) {
            return value.slice(0, index);
        }
    }
    return value;
}

function attribute(text, block, name) {
    const body = blockBody(text, block);
    const match = new RegExp(`(?:^|\\n)\\s*${name}\\s*=\\s*([^\\n]+)`, "m").exec(body);
    if (!match) return null;
    const value = removeInlineComment(match[1]).trim();
    const local = match.index + match[0].indexOf(name);
    return {value, from: block.open + 1 + local, to: block.open + 1 + local + name.length};
}

function directAttributes(text, block, blocks) {
    const bodyStart = block.open + 1;
    const body = blockBody(text, block);
    const attributes = [];
    const pattern = /(?:^|\n)\s*([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*([^\n]+)/g;
    let match;
    while ((match = pattern.exec(body))) {
        const from = bodyStart + match.index + match[0].indexOf(match[1]);
        const insideChild = blockChildren(blocks, block).some(child => from > child.open && from < child.end);
        if (insideChild) continue;
        attributes.push({
            name: match[1],
            value: removeInlineComment(match[2]).trim(),
            from,
            to: from + match[1].length,
        });
    }
    return attributes;
}

function attributeExpression(text, block, name) {
    const bodyStart = block.open + 1;
    const body = blockBody(text, block);
    const escapedName = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const match = new RegExp(`(?:^|\\n)\\s*${escapedName}\\s*=`, "m").exec(body);
    if (!match) return null;

    const from = bodyStart + match.index + match[0].indexOf(name);
    let cursor = bodyStart + match.index + match[0].length;
    while (/\s/.test(text[cursor] || "")) cursor++;
    const valueFrom = cursor;
    let square = 0;
    let curly = 0;
    let paren = 0;
    while (cursor < block.end - 1) {
        const char = text[cursor];
        if (char === '"' || char === "'") {
            cursor = skipQuoted(text, cursor);
            continue;
        }
        if (char === "#" || (char === "/" && text[cursor + 1] === "/")) {
            const newline = text.indexOf("\n", cursor);
            if (newline < 0 || (!square && !curly && !paren)) break;
            cursor = newline;
            continue;
        }
        if (char === "[") square++;
        if (char === "]") square--;
        if (char === "{") curly++;
        if (char === "}") {
            if (!curly) break;
            curly--;
        }
        if (char === "(") paren++;
        if (char === ")") paren--;
        if (char === "\n" && !square && !curly && !paren) break;
        cursor++;
    }
    return {
        name,
        value: text.slice(valueFrom, cursor).trim(),
        valueFrom,
        from,
        to: from + name.length,
    };
}

function requireAttributeExpression(diagnostics, text, block, name) {
    const found = attributeExpression(text, block, name);
    if (!found) diagnostics.push(issue(block, `Required attribute “${name}” is missing.`));
    return found;
}

function point(block) {
    return {from: block.from, to: Math.max(block.from + 1, block.open)};
}

function issue(block, message, severity = "error") {
    return {...point(block), severity, message};
}

function requireAttribute(diagnostics, text, block, name) {
    const found = attribute(text, block, name);
    if (!found) diagnostics.push(issue(block, `Required attribute “${name}” is missing.`));
    return found;
}

function validateChildNames(diagnostics, blocks, parent) {
    const allowed = allowedChildren[parent.type];
    if (!allowed) return;
    for (const child of blockChildren(blocks, parent)) {
        if (!allowed.has(child.type)) diagnostics.push(issue(child, `Block “${child.type}” is not valid inside ${parent.type}.`, "warning"));
    }
}

function validateUnlabelledSection(diagnostics, block) {
    if (block.labels.length) diagnostics.push(issue(block, `Section “${block.type}” does not accept labels.`));
}

function literalString(value) {
    if (!/^"(?:\\.|[^"\\\n])*"$/.test(value)) return null;
    try {
        return JSON.parse(value);
    } catch {
        return null;
    }
}

function validateCatalogKey(diagnostics, expression, key, namespace, description, referenceCatalog, version) {
    const matches = referenceCatalog[namespace].filter(item => item.key === key);
    const resolved = version === undefined
        ? matches.filter(item => Number(item.version || 0) === Math.max(...matches.map(candidate => Number(candidate.version || 0))))
        : matches.filter(item => Number(item.version || 0) === version);
    if (resolved.length === 0) {
        const scope = namespace === "node" ? " in the cluster" : (namespace === "space" ? "" : " in the selected space");
        const versionText = version === undefined ? "" : ` at version ${version}`;
        diagnostics.push({...expression, severity: "warning", message: `No ${description.toLowerCase()} named “${key}” exists${versionText}${scope}.`});
    }
}

function typedReference(expression, functionName) {
    const escaped = functionName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const match = new RegExp(`^${escaped}\\(\\s*(\"(?:\\\\.|[^\"\\\\\\n])+\")\\s*\\)$`).exec(expression.value);
    if (!match) return null;
    const key = literalString(match[1]);
    return key ? {key} : null;
}

function validateTypedReference(diagnostics, expression, functionName, namespace, description, referenceCatalog) {
    const reference = typedReference(expression, functionName);
    if (!reference) {
        diagnostics.push({...expression, severity: "error", message: `${description} must use ${functionName}(\"name\").`});
        return;
    }
    validateCatalogKey(diagnostics, expression, reference.key, namespace, description, referenceCatalog);
}

const referenceNamespaces = {
    secret: "secret",
    config: "config",
    asset: "asset",
};

function validateDeploymentReference(diagnostics, expression, functionName, referenceCatalog) {
    const call = parseCall(expression.value);
    const signature = `${functionName}("space", "deployment")`;
    if (!call || call.name !== functionName || call.args.length !== 2) {
        diagnostics.push({...expression, severity: "error", message: `Reference must use ${signature}.`});
        return;
    }
    const spaceName = literalString(call.args[0]);
    const deploymentName = literalString(call.args[1]);
    if (!spaceName || !deploymentName) {
        diagnostics.push({...expression, severity: "error", message: `${signature} requires two non-empty quoted names.`});
        return;
    }
    const space = referenceCatalog.space.find(item => item.key === spaceName);
    if (!space) {
        diagnostics.push({...expression, severity: "warning", message: `No space named “${spaceName}” exists.`});
        return;
    }
    const deployment = referenceCatalog.deployment.find(item => item.key === deploymentName
        && Number(item.spaceId) === Number(space.id));
    if (!deployment) {
        diagnostics.push({...expression, severity: "warning", message: `No deployment named “${deploymentName}” exists in space “${spaceName}”.`});
    }
}

function validateReferenceExpression(diagnostics, expression, referenceCatalog) {
    const call = parseCall(expression.value);
    if (!call || !["secret", "config", "asset", "address"].includes(call.name)) {
        diagnostics.push({...expression, severity: "error", message: "Value must be a string or a typed reference."});
        return;
    }
    const functionName = call.name;
    if (functionName === "address") {
        validateDeploymentReference(diagnostics, expression, functionName, referenceCatalog);
        return;
    }
    if (call.args.length < 1 || call.args.length > 2) {
        const signature = `${functionName}(\"name\"[, { version = number }])`;
        diagnostics.push({...expression, severity: "error", message: `Reference must use ${signature}.`});
        return;
    }
    const namespace = referenceNamespaces[functionName];
    const key = literalString(call.args[0]);
    if (key === null || key.length === 0) {
        diagnostics.push({...expression, severity: "error", message: `${functionName} requires a quoted name.`});
        return;
    }
    let version;
    if (call.args[1]) {
        version = validateVersionOptions(diagnostics, expression, call.args[1], `${functionName} reference`);
        if (version === null) return;
    }
    const resource = `${functionName[0].toUpperCase()}${functionName.slice(1)}`;
    validateCatalogKey(diagnostics, expression, key, namespace, resource, referenceCatalog, version);
}

function splitTopLevel(value) {
    const parts = [];
    let start = 0;
    let round = 0;
    let square = 0;
    let curly = 0;
    for (let index = 0; index < value.length; index++) {
        const char = value[index];
        if (char === '"' || char === "'") {
            index = skipQuoted(value, index) - 1;
            continue;
        }
        if (char === "(") round++;
        if (char === ")") round--;
        if (char === "[") square++;
        if (char === "]") square--;
        if (char === "{") curly++;
        if (char === "}") curly--;
        if (char === "," && round === 0 && square === 0 && curly === 0) {
            const part = value.slice(start, index).trim();
            if (part) parts.push(part);
            start = index + 1;
        }
    }
    const last = value.slice(start).trim();
    if (last) parts.push(last);
    return parts;
}

function parseCall(value) {
    const match = /^([A-Za-z_][A-Za-z0-9_]*)\s*\(([\s\S]*)\)$/.exec(value.trim());
    if (!match) return null;
    return {name: match[1], args: splitTopLevel(match[2])};
}

function validateVersionOptions(diagnostics, expression, value, description) {
    if (!/^\{[\s\S]*\}$/.test(value)) {
        diagnostics.push({...expression, severity: "error", message: `${description} options must be an object.`});
        return null;
    }
    const body = value.slice(1, -1);
    const options = [...body.matchAll(/(?:^|[\n,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^,\n}]+)/g)];
    let version = null;
    for (const option of options) {
        if (option[1] !== "version") {
            diagnostics.push({...expression, severity: "warning", message: `Option “${option[1]}” is not valid for ${description}.`});
        } else if (!/^\d+$/.test(option[2].trim()) || Number(option[2]) < 1) {
            diagnostics.push({...expression, severity: "error", message: `${description} version must be a positive integer.`});
        } else {
            version = Number(option[2]);
        }
    }
    if (version === null) diagnostics.push({...expression, severity: "error", message: `${description} options must contain version.`});
    return version;
}

function validateMountOptions(diagnostics, expression, value, sourceType) {
    if (!/^\{[\s\S]*\}$/.test(value)) {
        diagnostics.push({...expression, severity: "error", message: "Mount options must be an object."});
        return;
    }
    const allowed = sourceType === "asset" ? new Set(["executable"])
        : sourceType === "deployment" ? new Set(["read_only"]) : new Set();
    const body = value.slice(1, -1);
    const options = [...body.matchAll(/(?:^|[\n,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^,\n}]+)/g)];
    for (const option of options) {
        if (!allowed.has(option[1])) {
            diagnostics.push({...expression, severity: "warning", message: `Option “${option[1]}” is not valid for ${sourceType} mounts.`});
        } else if (!/^(?:true|false)$/.test(option[2].trim())) {
            diagnostics.push({...expression, severity: "error", message: `Mount option “${option[1]}” must be a boolean.`});
        }
    }
}

function validateMountsExpression(diagnostics, expression, referenceCatalog) {
    if (!/^\[[\s\S]*\]$/.test(expression.value)) {
        diagnostics.push({...expression, severity: "error", message: "Mounts must be a list of mount(...) expressions."});
        return;
    }
    let defaultVolumeCount = 0;
    for (const value of splitTopLevel(expression.value.slice(1, -1))) {
        const mountCall = parseCall(value);
        if (!mountCall || mountCall.name !== "mount" || mountCall.args.length < 2 || mountCall.args.length > 3) {
            diagnostics.push({...expression, severity: "error", message: 'Each mount must use mount(source, "/container/path"[, options]).'});
            continue;
        }

        const source = parseCall(mountCall.args[0]);
        let sourceType = "";
        if (source?.name === "default_volume" && source.args.length === 0) {
            sourceType = "default_volume";
            defaultVolumeCount++;
            if (defaultVolumeCount > 1) diagnostics.push({...expression, severity: "error", message: "Only one default volume mount is allowed."});
        } else if (source?.name === "asset" && source.args.length >= 1 && source.args.length <= 2) {
            sourceType = "asset";
            const key = literalString(source.args[0]);
            const version = source.args[1]
                ? validateVersionOptions(diagnostics, expression, source.args[1], "asset reference")
                : undefined;
            if (key && version !== null) {
                validateCatalogKey(diagnostics, expression, key, "asset", "Asset", referenceCatalog, version);
            } else {
                diagnostics.push({...expression, severity: "error", message: 'Asset mount sources must use asset("name"[, { version = number }]).'});
            }
        } else if (source?.name === "deployment") {
            sourceType = "deployment";
            validateDeploymentReference(diagnostics, {...expression, value: mountCall.args[0]}, "deployment", referenceCatalog);
        } else {
            diagnostics.push({...expression, severity: "error", message: "Mount source must be default_volume(), asset(\"name\"), or deployment(\"space\", \"deployment\")."});
        }

        if (literalString(mountCall.args[1]) === null) {
            diagnostics.push({...expression, severity: "error", message: "Mount container path must be a quoted string."});
        }
        if (mountCall.args[2] && sourceType) validateMountOptions(diagnostics, expression, mountCall.args[2], sourceType);
    }
}

function validPort(value) {
    return /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 65535;
}

function validateHostPortOptions(diagnostics, expression, value, description) {
    if (!/^\{[\s\S]*\}$/.test(value)) {
        diagnostics.push({...expression, severity: "error", message: `${description} options must be an object.`});
        return;
    }
    const body = value.slice(1, -1);
    const options = [...body.matchAll(/(?:^|[\n,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^,\n}]+)/g)];
    for (const option of options) {
        if (option[1] !== "host_port") {
            diagnostics.push({...expression, severity: "warning", message: `Option “${option[1]}” is not valid for ${description}.`});
        } else if (!validPort(option[2].trim())) {
            diagnostics.push({...expression, severity: "error", message: `${description} host_port must be an integer from 1 to 65535.`});
        }
    }
}

function validateIngressExpression(diagnostics, expression) {
    if (!/^\[[\s\S]*\]$/.test(expression.value)) {
        diagnostics.push({...expression, severity: "error", message: "Ingress must be a list of route functions."});
        return 0;
    }
    const routes = splitTopLevel(expression.value.slice(1, -1));
    for (const value of routes) {
        const route = parseCall(value);
        if (route?.name === "port_forward") {
            if (route.args.length < 2 || route.args.length > 3) {
                diagnostics.push({...expression, severity: "error", message: 'Port forwards must use port_forward("tcp"|"udp", container_port[, options]).'});
                continue;
            }
            const protocol = literalString(route.args[0]);
            if (protocol !== "tcp" && protocol !== "udp") {
                diagnostics.push({...expression, severity: "error", message: 'Port-forward protocol must be “tcp” or “udp”.'});
            }
            if (!validPort(route.args[1])) diagnostics.push({...expression, severity: "error", message: "Port-forward container port must be an integer from 1 to 65535."});
            if (route.args[2]) validateHostPortOptions(diagnostics, expression, route.args[2], "Port forward");
            continue;
        }
        if (route?.name === "tls_passthrough") {
            if (route.args.length < 2 || route.args.length > 3) {
                diagnostics.push({...expression, severity: "error", message: 'TLS routes must use tls_passthrough("hostname", container_port[, options]).'});
                continue;
            }
            if (literalString(route.args[0]) === null) diagnostics.push({...expression, severity: "error", message: "TLS passthrough hostname must be a quoted string."});
            if (!validPort(route.args[1])) diagnostics.push({...expression, severity: "error", message: "TLS passthrough container port must be an integer from 1 to 65535."});
            if (route.args[2]) validateHostPortOptions(diagnostics, expression, route.args[2], "TLS passthrough");
            continue;
        }
        diagnostics.push({...expression, severity: "error", message: "Ingress routes must use port_forward(...) or tls_passthrough(...)."});
    }
    return routes.length;
}

function sectionBlocks(diagnostics, blocks, parent, type) {
    const sections = blockChildren(blocks, parent, type);
    if (sections.length > 1) diagnostics.push(issue(sections[1], `Only one ${type} section is allowed.`));
    if (sections[0]) validateUnlabelledSection(diagnostics, sections[0]);
    return sections[0];
}

export function validateDeploymentHcl(text, referenceCatalog = referenceCatalogForDocument(text)) {
    const diagnostics = [];
    const {blocks, unmatched} = scanBlocks(text);
    const roots = blocks.filter(block => !block.parent);
    const deployments = roots.filter(block => block.type === "deployment");

    if (unmatched) {
        diagnostics.push({from: Math.max(0, text.length - 1), to: text.length, severity: "error", message: "Unclosed block or object."});
    }
    for (const root of roots.filter(block => block.type !== "deployment")) {
        diagnostics.push(issue(root, "Only deployment blocks are allowed at the root."));
    }
    if (deployments.length === 0) {
        diagnostics.push({from: 0, to: Math.min(1, text.length), severity: "error", message: "Add one deployment block."});
        return diagnostics;
    }
    if (deployments.length > 1) {
        for (const deployment of deployments.slice(1)) diagnostics.push(issue(deployment, "This editor accepts one deployment per file."));
    }

    const deployment = deployments[0];
    if (deployment.labels.length) diagnostics.push(issue(deployment, "Deployment identity belongs in the identity block, not a block label."));
    validateChildNames(diagnostics, blocks, deployment);
    const node = requireAttributeExpression(diagnostics, text, deployment, "node");
    if (node) validateTypedReference(diagnostics, node, "node", "node", "Node", referenceCatalog);

    const identities = blockChildren(blocks, deployment, "identity");
    if (identities.length !== 1) diagnostics.push(issue(deployment, "Deployment requires exactly one identity block."));
    if (identities[0]) {
        const identity = identities[0];
        validateUnlabelledSection(diagnostics, identity);
        const name = requireAttribute(diagnostics, text, identity, "name");
        if (name && !/^"(?:\\.|[^"\\])+"$/.test(name.value)) diagnostics.push({...name, severity: "error", message: "Deployment name must be a quoted string."});
        const space = requireAttributeExpression(diagnostics, text, identity, "space");
        if (space) validateTypedReference(diagnostics, space, "space", "space", "Space", referenceCatalog);
    }

    const containers = blockChildren(blocks, deployment, "container");
    if (containers.length === 0) diagnostics.push(issue(deployment, "Deployment requires at least one container block."));
    for (const container of containers) {
        validateUnlabelledSection(diagnostics, container);
        validateChildNames(diagnostics, blocks, container);

        const sources = blockChildren(blocks, container, "source");
        if (sources.length !== 1) diagnostics.push(issue(container, "Container requires exactly one source block."));
        if (sources[0]) {
            const source = sources[0];
            validateUnlabelledSection(diagnostics, source);
            validateChildNames(diagnostics, blocks, source);
            const variants = blockChildren(blocks, source);
            if (variants.length !== 1) diagnostics.push(issue(source, "Source requires exactly one container_image or nix_docker_build block."));
            const variant = variants[0];
            if (variant) {
                validateUnlabelledSection(diagnostics, variant);
                if (variant.type === "container_image") {
                    requireAttribute(diagnostics, text, variant, "image");
                } else if (variant.type === "nix_docker_build") {
                    requireAttribute(diagnostics, text, variant, "repo");
                    requireAttribute(diagnostics, text, variant, "flake");
                    const target = attribute(text, variant, "target");
                    if (target && !/^"\.#[^"]+"$/.test(target.value)) diagnostics.push({...target, severity: "error", message: "Nix target must be a quoted local selector beginning with .#."});
                }
            }
        }

        sectionBlocks(diagnostics, blocks, container, "process");

        const envVars = sectionBlocks(diagnostics, blocks, container, "env_vars");
        if (envVars) {
            validateChildNames(diagnostics, blocks, envVars);
            const entries = directAttributes(text, envVars, blocks);
            for (const entry of entries) {
                if (/^"(?:\\.|[^"\\])*"$/.test(entry.value)) continue;
                validateReferenceExpression(diagnostics, entry, referenceCatalog);
            }
        }

        const mounts = attributeExpression(text, container, "mounts");
        if (mounts) validateMountsExpression(diagnostics, mounts, referenceCatalog);

        sectionBlocks(diagnostics, blocks, container, "resources");
        const upgrade = sectionBlocks(diagnostics, blocks, container, "upgrade");
        if (upgrade) {
            const strategy = attribute(text, upgrade, "strategy");
            if (strategy && !/^"(?:recreate|rollover)"$/.test(strategy.value)) diagnostics.push({...strategy, severity: "error", message: 'Upgrade strategy must be “recreate” or “rollover”.'});
        }

        const version = requireAttribute(diagnostics, text, container, "version");
        if (version && !/^"[^"\n]+"$/.test(version.value)) diagnostics.push({...version, severity: "error", message: "Container version must be a quoted string."});
    }

    const networks = blockChildren(blocks, deployment, "network");
    if (networks.length !== 1) diagnostics.push(issue(deployment, "Deployment requires exactly one network block."));
    if (networks[0]) {
        const network = networks[0];
        validateUnlabelledSection(diagnostics, network);
        validateChildNames(diagnostics, blocks, network);
        const mode = requireAttribute(diagnostics, text, network, "mode");
        if (mode && !/^"(?:virtual|host)"$/.test(mode.value)) diagnostics.push({...mode, severity: "error", message: 'Network mode must be “virtual” or “host”.'});

        const ingress = attributeExpression(text, network, "ingress");
        const ingressRouteCount = ingress ? validateIngressExpression(diagnostics, ingress) : 0;

        if (mode?.value === '"host"' && ingressRouteCount) {
            diagnostics.push(issue(network, "Host networking cannot publish ports or ingress routes."));
        }
    }

    const desiredRunning = requireAttribute(diagnostics, text, deployment, "desired_running");
    if (desiredRunning && !/^(?:true|false)$/.test(desiredRunning.value)) diagnostics.push({...desiredRunning, severity: "error", message: "desired_running must be true or false."});
    return diagnostics;
}

export function formatHcl(text) {
    let depth = 0;
    const formatted = text.split("\n").map(rawLine => {
        const line = rawLine.trim();
        if (!line) return "";
        if (line.startsWith("}") || line.startsWith("]")) depth = Math.max(0, depth - 1);
        const result = `${"  ".repeat(depth)}${line}`;
        if ((line.endsWith("{") || line.endsWith("[")) && !line.startsWith("#")) depth++;
        return result;
    });
    return `${formatted.join("\n").replace(/\n{3,}/g, "\n\n").trim()}\n`;
}
