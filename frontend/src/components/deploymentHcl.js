import {FULL_GIT_COMMIT_RE, validateLocalFlakePath} from "./deploymentSource.js";

const NETWORK_VIRTUAL = 1;
const NETWORK_HOST = 2;
const PROTOCOL_TCP = 1;
const PROTOCOL_UDP = 2;
const INGRESS_TLS_PASSTHROUGH = 1;
const UPGRADE_RECREATE = 1;
const UPGRADE_ROLLOVER = 2;
const PERMISSION_READ_WRITE = 1;
const PERMISSION_READ_ONLY = 2;
const PERMISSION_READ_EXECUTE = 3;
const DEFAULT_DATA_PATH = "/data";

export const deploymentHclCompletionOptions = [
    {label: "deployment", type: "keyword", info: "Root deployment block"},
    {label: "identity", type: "keyword", info: "Deployment name and space"},
    {label: "container", type: "keyword", info: "Container workload"},
    {label: "source", type: "keyword", info: "Container source"},
    {label: "container_image", type: "keyword", info: "Existing container image"},
    {label: "nix_docker_build", type: "keyword", info: "Nix-built container image"},
    {label: "process", type: "keyword", info: "Container process settings"},
    {label: "env_vars", type: "keyword", info: "Environment variables"},
    {label: "resources", type: "keyword", info: "Container resource overrides"},
    {label: "upgrade", type: "keyword", info: "Container upgrade policy"},
    {label: "network", type: "keyword", info: "Deployment networking"},
    {label: "space", type: "function", info: "Resolve a space by name"},
    {label: "node", type: "function", info: "Resolve a node by name"},
    {label: "secret", type: "function", info: "Resolve a secret by name and optional version options"},
    {label: "config", type: "function", info: "Resolve a config by name and optional version options"},
    {label: "asset", type: "function", info: "Resolve an asset by key and optional version options"},
    {label: "address", type: "function", info: "Resolve an address by space and deployment name"},
    {label: "mount", type: "function", info: "Mount a typed source"},
    {label: "default_volume", type: "function", info: "The deployment default volume"},
    {label: "deployment", type: "function", info: "Resolve a default volume by space and deployment name"},
    {label: "host_path", type: "function", info: "A host bind-mount path"},
    {label: "port_forward", type: "function", info: "Publish a TCP or UDP port"},
    {label: "tls_passthrough", type: "function", info: "Route TLS by SNI"},
];

class ParseFailure extends Error {
    constructor(from, to, message) {
        super(message);
        this.from = from;
        this.to = to;
    }
}

function boundedRange(length, from, to) {
    const safeFrom = Math.max(0, Math.min(length, Number.isFinite(from) ? from : 0));
    const safeTo = Math.max(safeFrom, Math.min(length, Number.isFinite(to) ? to : safeFrom));
    return {from: safeFrom, to: safeTo};
}

function diagnostic(text, target, message, severity = "error") {
    const range = boundedRange(text.length, target?.from ?? 0, target?.to ?? Math.min(1, text.length));
    return {...range, severity, message};
}

function tokenize(text) {
    const tokens = [];
    let index = 0;
    const fail = (from, to, message) => { throw new ParseFailure(from, to, message); };

    while (index < text.length) {
        const char = text[index];
        if (/\s/.test(char)) {
            index++;
            continue;
        }
        if (char === "#" || (char === "/" && text[index + 1] === "/")) {
            const newline = text.indexOf("\n", index + 1);
            index = newline < 0 ? text.length : newline + 1;
            continue;
        }
        if (char === "/" && text[index + 1] === "*") {
            const end = text.indexOf("*/", index + 2);
            if (end < 0) fail(index, text.length, "Unclosed block comment.");
            index = end + 2;
            continue;
        }
        if (char === '"') {
            const from = index++;
            let closed = false;
            while (index < text.length) {
                if (text[index] === "\n" || text[index] === "\r") {
                    fail(from, index, "Quoted strings cannot contain a literal newline.");
                }
                if (text[index] === "\\") {
                    index += 2;
                    continue;
                }
                if (text[index] === '"') {
                    index++;
                    closed = true;
                    break;
                }
                index++;
            }
            if (!closed) fail(from, text.length, "Unclosed quoted string.");
            const raw = text.slice(from, index);
            let value;
            try {
                value = JSON.parse(raw);
            } catch {
                fail(from, index, "String must use valid JSON escaping.");
            }
            tokens.push({type: "string", value, from, to: index});
            continue;
        }
        if (/[A-Za-z_]/.test(char)) {
            const from = index++;
            while (index < text.length && /[A-Za-z0-9_-]/.test(text[index])) index++;
            tokens.push({type: "identifier", value: text.slice(from, index), from, to: index});
            continue;
        }
        if (/[0-9]/.test(char) || (char === "-" && /[0-9]/.test(text[index + 1] || ""))) {
            const from = index++;
            while (index < text.length && /[0-9]/.test(text[index])) index++;
            tokens.push({type: "number", value: text.slice(from, index), from, to: index});
            continue;
        }
        if ("{}[](),=".includes(char)) {
            tokens.push({type: char, value: char, from: index, to: index + 1});
            index++;
            continue;
        }
        fail(index, index + 1, `Unexpected character ${JSON.stringify(char)}.`);
    }
    tokens.push({type: "eof", value: "", from: text.length, to: text.length});
    return tokens;
}

class Parser {
    constructor(text) {
        this.text = text;
        this.tokens = tokenize(text);
        this.index = 0;
    }

    current() {
        return this.tokens[this.index];
    }

    take(type, message) {
        const token = this.current();
        if (token.type !== type) {
            throw new ParseFailure(token.from, token.to, message || `Expected ${type}.`);
        }
        this.index++;
        return token;
    }

    accept(type) {
        if (this.current().type !== type) return null;
        return this.tokens[this.index++];
    }

    parse() {
        const body = [];
        while (this.current().type !== "eof") body.push(this.statement());
        return {kind: "document", body, from: 0, to: this.text.length};
    }

    statement() {
        const name = this.take("identifier", "Expected an attribute or block name.");
        if (this.accept("=")) {
            const value = this.expression();
            return {kind: "attribute", name: name.value, nameToken: name, value, from: name.from, to: value.to};
        }
        if (this.accept("{")) return this.blockBody(name);
        const token = this.current();
        throw new ParseFailure(token.from, token.to, `Expected '=' or '{' after ${name.value}. Block labels are not supported.`);
    }

    blockBody(name) {
        const body = [];
        while (this.current().type !== "}" && this.current().type !== "eof") body.push(this.statement());
        const close = this.take("}", `Block ${name.value} is not closed.`);
        return {kind: "block", name: name.value, nameToken: name, body, from: name.from, to: close.to};
    }

    expression() {
        const token = this.current();
        if (token.type === "string") {
            this.index++;
            return {kind: "string", value: token.value, from: token.from, to: token.to};
        }
        if (token.type === "number") {
            this.index++;
            const value = Number(token.value);
            if (!Number.isSafeInteger(value)) throw new ParseFailure(token.from, token.to, "Number must be a safe integer.");
            return {kind: "number", value, from: token.from, to: token.to};
        }
        if (token.type === "identifier") {
            this.index++;
            if ((token.value === "true" || token.value === "false") && this.current().type !== "(") {
                return {kind: "boolean", value: token.value === "true", from: token.from, to: token.to};
            }
            this.take("(", `Expected '(' after ${token.value}.`);
            return this.call(token);
        }
        if (this.accept("[")) return this.list(token.from);
        if (this.accept("{")) return this.object(token.from);
        throw new ParseFailure(token.from, token.to, "Expected a string, integer, boolean, list, object, or function call.");
    }

    call(name) {
        const args = [];
        if (this.current().type !== ")") {
            while (true) {
                args.push(this.expression());
                if (!this.accept(",")) break;
                if (this.current().type === ")") break;
            }
        }
        const close = this.take(")", `Call to ${name.value} must separate arguments with commas and end with ')'.`);
        return {kind: "call", name: name.value, args, from: name.from, to: close.to};
    }

    list(from) {
        const items = [];
        if (this.current().type !== "]") {
            while (true) {
                items.push(this.expression());
                if (!this.accept(",")) break;
                if (this.current().type === "]") break;
            }
        }
        const close = this.take("]", "List items must be separated with commas and the list must end with ']'.");
        return {kind: "list", items, from, to: close.to};
    }

    object(from) {
        const entries = [];
        while (this.current().type !== "}" && this.current().type !== "eof") {
            const name = this.current().type === "string"
                ? this.take("string")
                : this.take("identifier", "Expected an option name or quoted key in the object.");
            this.take("=", `Expected '=' after option ${name.value}.`);
            const value = this.expression();
            entries.push({kind: "attribute", name: name.value, nameToken: name, value, from: name.from, to: value.to});
            this.accept(",");
        }
        const close = this.take("}", "Object is not closed.");
        return {kind: "object", entries, from, to: close.to};
    }
}

function unwrap(value) {
    let current = value;
    const seen = new Set();
    while (current && typeof current === "object" && !Array.isArray(current) && "val" in current && !seen.has(current)) {
        seen.add(current);
        current = current.val;
    }
    return current;
}

// The generic name+version resolvers want one catalog entry per referenceable
// asset version, but asset metas arrive one per asset with a version_refs
// index. Expand them so `id` is always the pinnable version row id.
function expandAssetVersions(metas) {
    const out = [];
    for (const meta of metas) {
        for (const ref of meta.versionRefs || []) {
            if (!Number(ref?.id || 0)) continue;
            out.push({id: Number(ref.id), key: meta.key, spaceId: meta.spaceId, version: Number(ref.version || 0)});
        }
    }
    return out;
}

function normalizedCatalogs(catalogs) {
    const source = unwrap(catalogs) || {};
    const list = name => {
        const value = unwrap(source[name]);
        return Array.isArray(value) ? value.filter(Boolean) : [];
    };
    return {
        spaces: list("spaces"),
        nodes: list("nodes"),
        assets: expandAssetVersions(list("assets")),
        secretRefs: list("secretRefs"),
        configRefs: list("configRefs"),
        deployments: list("deployments"),
    };
}

function deploymentConfig(item) {
    return item?.config || item;
}

function itemSpace(item, type) {
    if (type === "deployment") return deploymentConfig(item)?.identity?.spaceId;
    return item?.spaceId;
}

function itemName(item, type) {
    if (type === "asset") return item?.key;
    if (type === "deployment") return deploymentConfig(item)?.identity?.name;
    return item?.name;
}

function itemID(item, type) {
    if (type === "deployment") return deploymentConfig(item)?.id;
    return item?.id;
}

function isVersionedResource(type) {
    return type === "asset" || type === "secret" || type === "config";
}

function scopedItems(items, type, spaceId) {
    return items.filter(item => {
        if (item?.deleted || deploymentConfig(item)?.deleted) return false;
        const candidateSpace = itemSpace(item, type);
        return candidateSpace === undefined || candidateSpace === null || spaceId === undefined || spaceId === null
            || Number(candidateSpace) === Number(spaceId);
    });
}

function uniqueByID(items, type) {
    const result = new Map();
    for (const item of items) result.set(String(itemID(item, type)), item);
    return [...result.values()];
}

function findByID(items, type, id, spaceId) {
    return scopedItems(items, type, spaceId).find(item => String(itemID(item, type)) === String(id));
}

function placeholder(type, id) {
    const suffix = id === undefined || id === null || id === "" ? "missing" : String(id);
    return `__unresolved_${type}_${suffix}`;
}

function quote(value) {
    return JSON.stringify(String(value ?? ""));
}

function nameForID(catalogs, type, id, spaceId) {
    const collection = type === "space" ? catalogs.spaces
        : type === "node" ? catalogs.nodes
            : type === "asset" ? catalogs.assets
                : type === "secret" ? catalogs.secretRefs
                    : type === "config" ? catalogs.configRefs
                        : catalogs.deployments;
    const item = findByID(collection, type, id, type === "deployment" ? spaceId : undefined);
    return itemName(item, type) || placeholder(type, id);
}

function versionedReferenceForID(catalogs, type, id, pinVersions) {
    const collection = type === "asset" ? catalogs.assets
        : type === "secret" ? catalogs.secretRefs : catalogs.configRefs;
    const item = findByID(collection, type, id);
    const name = itemName(item, type) || placeholder(type, id);
    const version = Number(item?.version || 0);
    const latestVersion = scopedItems(collection, type)
        .filter(candidate => itemName(candidate, type) === name)
        .reduce((latest, candidate) => Math.max(latest, Number(candidate?.version || 0)), 0);
    const pinnedVersion = version > 0 && (pinVersions || version < latestVersion)
        ? `, { version = ${version} }`
        : "";
    return `${type}(${quote(name)}${pinnedVersion})`;
}

function deploymentReferenceForID(catalogs, functionName, id) {
    const item = findByID(catalogs.deployments, "deployment", id);
    const name = itemName(item, "deployment") || placeholder("deployment", id);
    const referenceSpaceId = itemSpace(item, "deployment");
    const space = nameForID(catalogs, "space", referenceSpaceId);
    return `${functionName}(${quote(space)}, ${quote(name)})`;
}

function envValueToHcl(value, catalogs, spaceId, pinVersions) {
    if (value?.secretVersionId !== undefined && value.secretVersionId !== null) {
        return versionedReferenceForID(catalogs, "secret", value.secretVersionId, pinVersions);
    }
    if (value?.configVersionId !== undefined && value.configVersionId !== null) {
        return versionedReferenceForID(catalogs, "config", value.configVersionId, pinVersions);
    }
    if (value?.addressDeploymentId !== undefined && value.addressDeploymentId !== null) {
        return deploymentReferenceForID(catalogs, "address", value.addressDeploymentId);
    }
    if (value?.assetVersionId || value?.asset) {
        return versionedReferenceForID(catalogs, "asset", value.assetVersionId, pinVersions);
    }
    return quote(value?.value ?? "");
}

function imageReferenceVersion(raw) {
    let image = String(raw || "").trim();
    image = image.replace(/^docker:\/\//, "").replace(/^https?:\/\//, "").replace(/\/$/, "");
    const digestIndex = image.indexOf("@");
    if (digestIndex >= 0) return image.slice(digestIndex + 1);
    const lastSlash = image.lastIndexOf("/");
    const lastColon = image.lastIndexOf(":");
    return lastColon > lastSlash ? image.slice(lastColon + 1) : "";
}

function mountOption(name, value) {
    return value ? `, { ${name} = true }` : "";
}

export function deploymentDocumentToHcl(document, catalogs = {}, options = {}) {
    const refs = normalizedCatalogs(catalogs);
    const doc = document || {};
    const identity = doc.identity || {};
    const spec = doc.spec || {};
    const container = spec.container1Spec || {};
    const source = container.source || {};
    const runtime = container.runtime || {};
    const defaultVolume = runtime.defaultVolume || {};
    const networking = spec.networking || {};
    const spaceId = identity.spaceId;
    const pinVersions = Boolean(options.pinVersions);
    const lines = [];
    const add = (depth, line = "") => lines.push(`${"  ".repeat(depth)}${line}`);

    add(0, "deployment {");
    add(1, `node = node(${quote(nameForID(refs, "node", doc.nodeId))})`);
    add(0);
    add(1, "identity {");
    add(2, `name = ${quote(identity.name)}`);
    add(2, `space = space(${quote(nameForID(refs, "space", spaceId))})`);
    add(1, "}");
    add(0);
    add(1, "container {");
    add(2, "source {");
    if (source.nixDockerBuild) {
        const nixSource = source.nixDockerBuild;
        add(3, "nix_docker_build {");
        add(4, `repo = ${quote(nixSource.repo)}`);
        add(4, `flake = ${quote(nixSource.flake)}`);
        if (nixSource.target) add(4, `target = ${quote(nixSource.target)}`);
        add(3, "}");
    } else {
        add(3, "container_image {");
        add(4, `image = ${quote(source.remoteImage?.image)}`);
        add(3, "}");
    }
    add(2, "}");

    const hasProcess = Boolean(runtime.user || runtime.overrideWorkingDir || (runtime.overrideCommand || []).length
        || (defaultVolume.disabled && defaultVolume.containerPath));
    if (hasProcess) {
        add(0);
        add(2, "process {");
        if (runtime.user) add(3, `user = ${quote(runtime.user)}`);
        if ((runtime.overrideCommand || []).length) add(3, `command = [${runtime.overrideCommand.map(quote).join(", ")}]`);
        if (runtime.overrideWorkingDir) add(3, `working_dir = ${quote(runtime.overrideWorkingDir)}`);
        if (defaultVolume.disabled && defaultVolume.containerPath) add(3, `data_mount_path = ${quote(defaultVolume.containerPath)}`);
        add(2, "}");
    }

    const envVars = runtime.envVars || {};
    const envNames = Object.keys(envVars).sort();
    if (envNames.length) {
        add(0);
        add(2, "env_vars = {");
        for (const name of envNames) add(3, `${quote(name)} = ${envValueToHcl(envVars[name], refs, spaceId, pinVersions)}`);
        add(2, "}");
    }

    const mounts = [];
    if (!defaultVolume.disabled) {
        mounts.push(`mount(default_volume(), ${quote(defaultVolume.containerPath || DEFAULT_DATA_PATH)})`);
    }
    for (const mount of runtime.crossDeploymentMounts || []) {
        const mountSource = deploymentReferenceForID(refs, "deployment", mount?.deploymentId);
        mounts.push(`mount(${mountSource}, ${quote(mount?.containerPath)}${mountOption("read_only", mount?.permission === PERMISSION_READ_ONLY)})`);
    }
    for (const mount of runtime.mounts || []) {
        const mountSource = `host_path(${quote(mount?.hostPath)})`;
        mounts.push(`mount(${mountSource}, ${quote(mount?.containerPath)}${mountOption("read_only", mount?.permission === PERMISSION_READ_ONLY)})`);
    }
    for (const mount of runtime.assetMounts || []) {
        const mountSource = versionedReferenceForID(refs, "asset", mount?.assetVersionId, pinVersions);
        mounts.push(`mount(${mountSource}, ${quote(mount?.containerPath)}${mountOption("executable", mount?.permission === PERMISSION_READ_EXECUTE)})`);
    }
    if (mounts.length) {
        add(0);
        add(2, "mounts = [");
        for (const mount of mounts) add(3, `${mount},`);
        add(2, "]");
    }

    if (runtime.devShmSizeKb || runtime.fileDescriptorLimit) {
        add(0);
        add(2, "resources {");
        if (runtime.devShmSizeKb) add(3, `dev_shm_size_kb = ${Number(runtime.devShmSizeKb)}`);
        if (runtime.fileDescriptorLimit) add(3, `file_descriptor_limit = ${Number(runtime.fileDescriptorLimit)}`);
        add(2, "}");
    }

    if (container.upgradeStrategy === UPGRADE_ROLLOVER || container.readinessSignal) {
        add(0);
        add(2, "upgrade {");
        add(3, `strategy = ${quote(container.upgradeStrategy === UPGRADE_ROLLOVER ? "rollover" : "recreate")}`);
        if (container.readinessSignal) {
            add(3, `readiness_timeout_seconds = ${Number(container.readinessSignal.timeoutSeconds || 0)}`);
        }
        add(2, "}");
    }

    add(0);
    add(2, `version = ${quote(container.version)}`);
    add(1, "}");
    add(0);
    add(1, "network {");
    const mode = networking.mode === NETWORK_VIRTUAL ? "virtual"
        : networking.mode === NETWORK_HOST ? "host" : placeholder("network_mode", networking.mode);
    add(2, `mode = ${quote(mode)}`);
    const ingress = [];
    for (const route of networking.portForwarding || []) {
        const protocol = route?.protocol === PROTOCOL_UDP ? "udp" : "tcp";
        const containerPort = Number(route?.containerPort || 0);
        const hostPort = Number(route?.hostPort || 0);
        const options = hostPort && hostPort !== containerPort ? `, { host_port = ${hostPort} }` : "";
        ingress.push(`port_forward(${quote(protocol)}, ${containerPort}${options})`);
    }
    for (const route of networking.ingress || []) {
        const config = route?.tlsPassthroughConfig || {};
        const options = config.hostPort ? `, { host_port = ${Number(config.hostPort)} }` : "";
        ingress.push(`tls_passthrough(${quote(route?.hostname)}, ${Number(config.containerPort || 0)}${options})`);
    }
    if (ingress.length) {
        add(0);
        add(2, "ingress = [");
        for (const route of ingress) add(3, `${route},`);
        add(2, "]");
    }
    add(1, "}");
    add(0);
    add(1, `desired_running = ${container.running ? "true" : "false"}`);
    add(0, "}");
    return `${lines.join("\n")}\n`;
}

function members(parent, kind, name) {
    return parent?.body?.filter(item => item.kind === kind && (!name || item.name === name)) || [];
}

function firstAttribute(parent, name) {
    return members(parent, "attribute", name)[0] || null;
}

function firstBlock(parent, name) {
    return members(parent, "block", name)[0] || null;
}

function validateMembers(text, diagnostics, parent, allowedAttributes, allowedBlocks, dynamicAttributes = false) {
    const seen = new Set();
    for (const item of parent?.body || []) {
        const key = `${item.kind}:${item.name}`;
        if (seen.has(key)) diagnostics.push(diagnostic(text, item.nameToken, `${item.name} is declared more than once.`));
        seen.add(key);
        if (item.kind === "attribute" && !dynamicAttributes && !allowedAttributes.has(item.name)) {
            diagnostics.push(diagnostic(text, item.nameToken, `Attribute ${item.name} is not valid in ${parent.name || "the document"}.`));
        }
        if (item.kind === "block" && !allowedBlocks.has(item.name)) {
            diagnostics.push(diagnostic(text, item.nameToken, `Block ${item.name} is not valid in ${parent.name || "the document"}.`));
        }
    }
}

function requireAttribute(text, diagnostics, parent, name) {
    const attr = firstAttribute(parent, name);
    if (!attr) diagnostics.push(diagnostic(text, parent, `Required attribute ${name} is missing.`));
    return attr;
}

function exactlyOneBlock(text, diagnostics, parent, name) {
    const blocks = members(parent, "block", name);
    if (blocks.length !== 1) diagnostics.push(diagnostic(text, blocks[1] || parent, `${parent.name || "Document"} requires exactly one ${name} block.`));
    return blocks[0] || null;
}

function stringValue(text, diagnostics, attr, description, {nonempty = false} = {}) {
    if (!attr) return null;
    if (attr.value.kind !== "string") {
        diagnostics.push(diagnostic(text, attr.value, `${description} must be a quoted string.`));
        return null;
    }
    if (nonempty && !attr.value.value) {
        diagnostics.push(diagnostic(text, attr.value, `${description} cannot be empty.`));
        return null;
    }
    return attr.value.value;
}

function booleanValue(text, diagnostics, attr, description) {
    if (!attr) return null;
    if (attr.value.kind !== "boolean") {
        diagnostics.push(diagnostic(text, attr.value, `${description} must be true or false.`));
        return null;
    }
    return attr.value.value;
}

function integerValue(text, diagnostics, attr, description, minimum = 0, maximum = Number.MAX_SAFE_INTEGER) {
    if (!attr) return null;
    if (attr.value.kind !== "number" || attr.value.value < minimum || attr.value.value > maximum) {
        diagnostics.push(diagnostic(text, attr.value, `${description} must be an integer from ${minimum} to ${maximum}.`));
        return null;
    }
    return attr.value.value;
}

function resolveNamed(text, diagnostics, expression, type, name, catalogs, spaceId, options = {}) {
    const collection = type === "space" ? catalogs.spaces
        : type === "node" ? catalogs.nodes
            : type === "asset" ? catalogs.assets
                : type === "secret" ? catalogs.secretRefs
                    : type === "config" ? catalogs.configRefs
                        : catalogs.deployments;
    let matches = scopedItems(collection, type, type === "deployment" ? spaceId : undefined)
        .filter(item => itemName(item, type) === name);
    if (type === "deployment" && options.nodeId !== undefined && options.nodeId !== null) {
        matches = matches.filter(item => Number(deploymentConfig(item)?.nodeId) === Number(options.nodeId));
    }
    if (isVersionedResource(type)) {
        if (options.version !== undefined && options.version !== null) {
            matches = matches.filter(item => Number(item?.version || 0) === Number(options.version));
        } else {
            const latest = matches.reduce((version, item) => Math.max(version, Number(item?.version || 0)), -1);
            if (latest >= 0) matches = matches.filter(item => Number(item?.version || 0) === latest);
        }
    }
    matches = uniqueByID(matches, type);
    if (matches.length === 0) {
        const scope = type === "node" ? " in the cluster" : type === "deployment" ? " in the selected space" : "";
        diagnostics.push(diagnostic(text, expression, `No ${type} named ${quote(name)} exists${scope}.`));
        return null;
    }
    if (matches.length > 1) {
        diagnostics.push(diagnostic(text, expression, `${type[0].toUpperCase()}${type.slice(1)} reference ${quote(name)} is ambiguous.`));
        return null;
    }
    return matches[0];
}

function deploymentReference(text, diagnostics, expression, catalogs, nodeId) {
    if (expression.kind !== "call" || !["address", "deployment"].includes(expression.name)
        || expression.args.length !== 2
        || expression.args.some(argument => argument.kind !== "string" || !argument.value)) {
        diagnostics.push(diagnostic(text, expression, `${expression.name || "Deployment reference"} must use ${expression.name || "deployment"}("space", "deployment").`));
        return null;
    }
    const space = resolveNamed(text, diagnostics, expression.args[0], "space", expression.args[0].value, catalogs);
    if (!space) return null;
    return resolveNamed(text, diagnostics, expression, "deployment", expression.args[1].value, catalogs, Number(space.id), {nodeId});
}

function typedReference(text, diagnostics, attr, functionName, type, catalogs, spaceId) {
    if (!attr) return null;
    const expression = attr.value;
    if (expression.kind !== "call" || expression.name !== functionName || expression.args.length !== 1 || expression.args[0].kind !== "string" || !expression.args[0].value) {
        diagnostics.push(diagnostic(text, expression, `${attr.name} must use ${functionName}("name").`));
        return null;
    }
    return resolveNamed(text, diagnostics, expression, type, expression.args[0].value, catalogs, spaceId);
}

function validateObject(text, diagnostics, expression, allowed) {
    if (!expression || expression.kind !== "object") {
        diagnostics.push(diagnostic(text, expression, "Options must be an object."));
        return new Map();
    }
    const options = new Map();
    for (const entry of expression.entries) {
        if (options.has(entry.name)) diagnostics.push(diagnostic(text, entry.nameToken, `Option ${entry.name} is declared more than once.`));
        options.set(entry.name, entry);
        if (!allowed.has(entry.name)) diagnostics.push(diagnostic(text, entry.nameToken, `Option ${entry.name} is not valid here.`));
    }
    return options;
}

function optionBoolean(text, diagnostics, options, name) {
    const option = options.get(name);
    if (!option) return false;
    return booleanValue(text, diagnostics, option, `Option ${name}`) ?? false;
}

function referenceVersion(text, diagnostics, expression, description) {
    if (!expression) return undefined;
    const options = validateObject(text, diagnostics, expression, new Set(["version"]));
    const version = options.get("version");
    if (!version) {
        diagnostics.push(diagnostic(text, expression, `${description} options must contain version.`));
        return undefined;
    }
    return integerValue(text, diagnostics, version, `${description} version`, 1) ?? undefined;
}

function parseMounts(text, diagnostics, attr, catalogs, spaceId, nodeId, runtime) {
    runtime.defaultVolume ||= {};
    runtime.defaultVolume.disabled = true;
    if (!attr) return;
    if (attr.value.kind !== "list") {
        diagnostics.push(diagnostic(text, attr.value, "mounts must be a list of mount(...) calls."));
        return;
    }
    let defaultVolumes = 0;
    const mounts = [];
    const assetMounts = [];
    for (const expression of attr.value.items) {
        if (expression.kind !== "call" || expression.name !== "mount" || expression.args.length < 2 || expression.args.length > 3) {
            diagnostics.push(diagnostic(text, expression, 'Each mount must use mount(source, "/container/path"[, options]).'));
            continue;
        }
        const [source, pathExpression, optionsExpression] = expression.args;
        if (pathExpression.kind !== "string") {
            diagnostics.push(diagnostic(text, pathExpression, "Mount container path must be a quoted string."));
            continue;
        }
        if (source.kind !== "call") {
            diagnostics.push(diagnostic(text, source, "Mount source must be a typed function call."));
            continue;
        }
        if (source.name === "default_volume" && source.args.length === 0) {
            defaultVolumes++;
            if (defaultVolumes > 1) diagnostics.push(diagnostic(text, source, "Only one default volume mount is allowed."));
            if (optionsExpression) diagnostics.push(diagnostic(text, optionsExpression, "Default volume mounts do not accept options."));
            runtime.defaultVolume.disabled = false;
            if (pathExpression.value !== DEFAULT_DATA_PATH) runtime.defaultVolume.containerPath = pathExpression.value;
            continue;
        }
        if (source.name === "asset" && source.args.length >= 1 && source.args.length <= 2
            && source.args[0].kind === "string" && source.args[0].value) {
            const version = referenceVersion(text, diagnostics, source.args[1], "Asset reference");
            const asset = resolveNamed(text, diagnostics, source, "asset", source.args[0].value, catalogs, spaceId, {
                version,
            });
            const options = optionsExpression ? validateObject(text, diagnostics, optionsExpression, new Set(["executable"])) : new Map();
            if (asset) {
                assetMounts.push({
                    assetVersionId: Number(asset.id),
                    containerPath: pathExpression.value,
                    permission: optionBoolean(text, diagnostics, options, "executable")
                        ? PERMISSION_READ_EXECUTE
                        : PERMISSION_READ_ONLY,
                });
            }
            continue;
        }
        if (source.name === "deployment") {
            const options = optionsExpression ? validateObject(text, diagnostics, optionsExpression, new Set(["read_only"])) : new Map();
            const deployment = deploymentReference(text, diagnostics, source, catalogs, nodeId);
            if (!deployment) continue;
            mounts.push({
                deploymentId: Number(deploymentConfig(deployment).id),
                containerPath: pathExpression.value,
                permission: optionBoolean(text, diagnostics, options, "read_only")
                    ? PERMISSION_READ_ONLY
                    : PERMISSION_READ_WRITE,
            });
            continue;
        }
        if (source.name === "host_path" && source.args.length === 1 && source.args[0].kind === "string") {
            const options = optionsExpression ? validateObject(text, diagnostics, optionsExpression, new Set(["read_only"])) : new Map();
            mounts.push({
                hostPath: source.args[0].value,
                containerPath: pathExpression.value,
                permission: optionBoolean(text, diagnostics, options, "read_only")
                    ? PERMISSION_READ_ONLY
                    : PERMISSION_READ_WRITE,
            });
            continue;
        }
        diagnostics.push(diagnostic(text, source, 'Mount source must be default_volume(), asset("key"[, { version = number }]), deployment("space", "deployment"), or host_path("/host").'));
    }
    const crossDeploymentMounts = mounts.filter(mount => mount.deploymentId);
    const customMounts = mounts.filter(mount => mount.hostPath);
    if (crossDeploymentMounts.length) runtime.crossDeploymentMounts = crossDeploymentMounts;
    if (customMounts.length) runtime.mounts = customMounts;
    if (assetMounts.length) runtime.assetMounts = assetMounts;
}

function parseEnvVars(text, diagnostics, block, attr, catalogs, spaceId, nodeId, container) {
    if (!block && !attr) return;
    if (block && attr) diagnostics.push(diagnostic(text, attr, "env_vars must be declared only once."));
    if (block) validateMembers(text, diagnostics, block, new Set(), new Set(), true);
    if (attr && attr.value.kind !== "object") {
        diagnostics.push(diagnostic(text, attr.value, "env_vars must be an object."));
        return;
    }
    const envVars = {};
    const setEnv = (name, value) => Object.defineProperty(envVars, name, {
        value,
        enumerable: true,
        configurable: true,
        writable: true,
    });
    const entries = attr?.value?.entries || members(block, "attribute");
    for (const entry of entries) {
        if (Object.hasOwn(envVars, entry.name)) {
            diagnostics.push(diagnostic(text, entry.nameToken, `Environment variable ${quote(entry.name)} is declared more than once.`));
            continue;
        }
        const value = entry.value;
        if (value.kind === "string") {
            setEnv(entry.name, {value: value.value});
            continue;
        }
        if (value.kind !== "call" || value.args.length < 1 || value.args.length > 2 || value.args[0].kind !== "string" || !value.args[0].value
            || !["secret", "config", "asset", "address"].includes(value.name)) {
            diagnostics.push(diagnostic(text, value, 'Environment values must be strings or typed references such as secret("name", { version = 1 }) or address("space", "deployment").'));
            continue;
        }
        const type = value.name === "address" ? "deployment" : value.name;
        let item;
        if (value.name === "address") {
            item = deploymentReference(text, diagnostics, value, catalogs, nodeId);
        } else {
            const version = referenceVersion(text, diagnostics, value.args[1], `${value.name[0].toUpperCase()}${value.name.slice(1)} reference`);
            item = resolveNamed(text, diagnostics, value, type, value.args[0].value, catalogs, spaceId, {
                version,
            });
        }
        if (!item) continue;
        if (value.name === "secret") setEnv(entry.name, {secretVersionId: Number(item.id)});
        if (value.name === "config") setEnv(entry.name, {configVersionId: Number(item.id)});
        if (value.name === "asset") setEnv(entry.name, {asset: item.key, assetVersionId: Number(item.id)});
        if (value.name === "address") {
            const config = deploymentConfig(item);
            setEnv(entry.name, {addressDeploymentId: Number(config.id), addressSpaceId: Number(config.identity.spaceId)});
        }
    }
    container.envVars = envVars;
}

function parseIngress(text, diagnostics, attr, networking) {
    if (!attr) return 0;
    if (attr.value.kind !== "list") {
        diagnostics.push(diagnostic(text, attr.value, "ingress must be a list of route function calls."));
        return 0;
    }
    const portForwarding = [];
    const ingress = [];
    for (const route of attr.value.items) {
        if (route.kind === "call" && route.name === "port_forward" && route.args.length >= 2 && route.args.length <= 3) {
            const [protocol, containerPort, optionsExpression] = route.args;
            if (protocol.kind !== "string" || (protocol.value !== "tcp" && protocol.value !== "udp")) {
                diagnostics.push(diagnostic(text, protocol, 'Port-forward protocol must be "tcp" or "udp".'));
                continue;
            }
            if (containerPort.kind !== "number" || containerPort.value < 1 || containerPort.value > 65535) {
                diagnostics.push(diagnostic(text, containerPort, "Port-forward container port must be an integer from 1 to 65535."));
                continue;
            }
            const options = optionsExpression ? validateObject(text, diagnostics, optionsExpression, new Set(["host_port"])) : new Map();
            const hostPortEntry = options.get("host_port");
            const hostPort = hostPortEntry
                ? integerValue(text, diagnostics, hostPortEntry, "Port-forward host_port", 1, 65535)
                : containerPort.value;
            portForwarding.push({
                protocol: protocol.value === "tcp" ? PROTOCOL_TCP : PROTOCOL_UDP,
                hostPort: hostPort ?? containerPort.value,
                containerPort: containerPort.value,
            });
            continue;
        }
        if (route.kind === "call" && route.name === "tls_passthrough" && route.args.length >= 2 && route.args.length <= 3) {
            const [hostname, containerPort, optionsExpression] = route.args;
            if (hostname.kind !== "string" || !hostname.value) {
                diagnostics.push(diagnostic(text, hostname, "TLS passthrough hostname must be a non-empty quoted string."));
                continue;
            }
            if (containerPort.kind !== "number" || containerPort.value < 1 || containerPort.value > 65535) {
                diagnostics.push(diagnostic(text, containerPort, "TLS passthrough container port must be an integer from 1 to 65535."));
                continue;
            }
            const options = optionsExpression ? validateObject(text, diagnostics, optionsExpression, new Set(["host_port"])) : new Map();
            const hostPortEntry = options.get("host_port");
            const hostPort = hostPortEntry ? integerValue(text, diagnostics, hostPortEntry, "TLS passthrough host_port", 1, 65535) : 0;
            ingress.push({kind: INGRESS_TLS_PASSTHROUGH, hostname: hostname.value, tlsPassthroughConfig: {hostPort: hostPort ?? 0, containerPort: containerPort.value}});
            continue;
        }
        diagnostics.push(diagnostic(text, route, "Ingress routes must use port_forward(...) or tls_passthrough(...)."));
    }
    if (portForwarding.length) networking.portForwarding = portForwarding;
    if (ingress.length) networking.ingress = ingress;
    return attr.value.items.length;
}

function parseValidatedDocument(text, ast, catalogs, constraints, diagnostics) {
    validateMembers(text, diagnostics, ast, new Set(), new Set(["deployment"]));
    const roots = members(ast, "block", "deployment");
    if (roots.length !== 1) diagnostics.push(diagnostic(text, roots[1] || ast, "Document requires exactly one deployment block."));
    const deployment = roots[0];
    if (!deployment) return null;
    validateMembers(text, diagnostics, deployment, new Set(["node", "desired_running"]), new Set(["identity", "container", "network"]));

    const identity = exactlyOneBlock(text, diagnostics, deployment, "identity");
    let name = null;
    let spaceId = null;
    let nodeId = null;
    let nameAttr = null;
    const nodeAttr = requireAttribute(text, diagnostics, deployment, "node");
    const node = typedReference(text, diagnostics, nodeAttr, "node", "node", catalogs);
    nodeId = node ? Number(node.id) : null;
    if (identity) {
        validateMembers(text, diagnostics, identity, new Set(["name", "space"]), new Set());
        nameAttr = requireAttribute(text, diagnostics, identity, "name");
        const spaceAttr = requireAttribute(text, diagnostics, identity, "space");
        name = stringValue(text, diagnostics, nameAttr, "Deployment name", {nonempty: true});
        const space = typedReference(text, diagnostics, spaceAttr, "space", "space", catalogs);
        spaceId = space ? Number(space.id) : null;
    }

    const containers = members(deployment, "block", "container");
    if (containers.length !== 1) diagnostics.push(diagnostic(text, containers[1] || deployment, "Deployment requires exactly one container block."));
    const containerBlock = containers[0];
    const sourceSpec = {};
    const runtime = {defaultVolume: {disabled: true}};
    const container = {source: sourceSpec, runtime, upgradeStrategy: UPGRADE_RECREATE};
    let version = null;
    let versionAttr = null;
    if (containerBlock) {
        validateMembers(text, diagnostics, containerBlock, new Set(["env_vars", "mounts", "version"]), new Set(["source", "process", "env_vars", "resources", "upgrade"]));
        const source = exactlyOneBlock(text, diagnostics, containerBlock, "source");
        if (source) {
            validateMembers(text, diagnostics, source, new Set(), new Set(["container_image", "nix_docker_build"]));
            const variants = source.body.filter(item => item.kind === "block");
            if (variants.length !== 1 || !["container_image", "nix_docker_build"].includes(variants[0]?.name)) {
                diagnostics.push(diagnostic(text, variants[1] || source, "Source requires exactly one container_image or nix_docker_build block."));
            }
            const variant = variants[0];
            if (variant?.name === "container_image") {
                validateMembers(text, diagnostics, variant, new Set(["image"]), new Set());
                const image = stringValue(text, diagnostics, requireAttribute(text, diagnostics, variant, "image"), "Container image", {nonempty: true});
                if (image !== null) sourceSpec.remoteImage = {image};
            }
            if (variant?.name === "nix_docker_build") {
                validateMembers(text, diagnostics, variant, new Set(["repo", "flake", "target"]), new Set());
                const repo = stringValue(text, diagnostics, requireAttribute(text, diagnostics, variant, "repo"), "Nix repository", {nonempty: true});
                const flakeAttr = requireAttribute(text, diagnostics, variant, "flake");
                const flake = stringValue(text, diagnostics, flakeAttr, "Nix flake", {nonempty: true});
                if (flake !== null && !validateLocalFlakePath(flake).ok) {
                    diagnostics.push(diagnostic(text, flakeAttr?.value || flakeAttr, validateLocalFlakePath(flake).message));
                }
                const targetAttr = firstAttribute(variant, "target");
                const target = stringValue(text, diagnostics, targetAttr, "Nix target");
                if (target !== null && target !== "" && !target.startsWith(".#")) diagnostics.push(diagnostic(text, targetAttr.value, 'Nix target must begin with ".#".'));
                if (repo !== null && flake !== null) {
                    sourceSpec.nixDockerBuild = {repo, flake};
                    if (targetAttr && target !== null) sourceSpec.nixDockerBuild.target = target;
                }
            }
        }

        const process = firstBlock(containerBlock, "process");
        if (process) {
            validateMembers(text, diagnostics, process, new Set(["user", "command", "working_dir", "data_mount_path"]), new Set());
            const userAttr = firstAttribute(process, "user");
            const user = stringValue(text, diagnostics, userAttr, "Process user");
            if (userAttr && user !== null) runtime.user = user;
            const commandAttr = firstAttribute(process, "command");
            if (commandAttr) {
                if (commandAttr.value.kind !== "list" || commandAttr.value.items.some(item => item.kind !== "string")) {
                    diagnostics.push(diagnostic(text, commandAttr.value, "Process command must be a list of quoted strings."));
                } else {
                    runtime.overrideCommand = commandAttr.value.items.map(item => item.value);
                }
            }
            const workingDirAttr = firstAttribute(process, "working_dir");
            const workingDir = stringValue(text, diagnostics, workingDirAttr, "Process working_dir");
            if (workingDirAttr && workingDir !== null) runtime.overrideWorkingDir = workingDir;
            const dataMountPathAttr = firstAttribute(process, "data_mount_path");
            const dataMountPath = stringValue(text, diagnostics, dataMountPathAttr, "Process data_mount_path");
            if (dataMountPathAttr && dataMountPath !== null) runtime.defaultVolume.containerPath = dataMountPath;
        }

        parseEnvVars(text, diagnostics, firstBlock(containerBlock, "env_vars"), firstAttribute(containerBlock, "env_vars"), catalogs, spaceId, nodeId, runtime);
        parseMounts(text, diagnostics, firstAttribute(containerBlock, "mounts"), catalogs, spaceId, nodeId, runtime);

        const resources = firstBlock(containerBlock, "resources");
        if (resources) {
            validateMembers(text, diagnostics, resources, new Set(["dev_shm_size_kb", "file_descriptor_limit"]), new Set());
            const devShmAttr = firstAttribute(resources, "dev_shm_size_kb");
            const devShm = integerValue(text, diagnostics, devShmAttr, "dev_shm_size_kb", 1);
            if (devShmAttr && devShm !== null) runtime.devShmSizeKb = devShm;
            const limitAttr = firstAttribute(resources, "file_descriptor_limit");
            const limit = integerValue(text, diagnostics, limitAttr, "file_descriptor_limit", 1);
            if (limitAttr && limit !== null) runtime.fileDescriptorLimit = limit;
        }

        const upgrade = firstBlock(containerBlock, "upgrade");
        if (upgrade) {
            validateMembers(text, diagnostics, upgrade, new Set(["strategy", "readiness_timeout_seconds"]), new Set());
            const strategyAttr = firstAttribute(upgrade, "strategy");
            const strategy = stringValue(text, diagnostics, strategyAttr, "Upgrade strategy");
            if (strategyAttr && strategy !== "recreate" && strategy !== "rollover") {
                diagnostics.push(diagnostic(text, strategyAttr.value, 'Upgrade strategy must be "recreate" or "rollover".'));
            } else if (strategy === "rollover") {
                container.upgradeStrategy = UPGRADE_ROLLOVER;
            }
            const timeoutAttr = firstAttribute(upgrade, "readiness_timeout_seconds");
            const timeout = integerValue(text, diagnostics, timeoutAttr, "readiness_timeout_seconds", 0);
            if (timeoutAttr && timeout !== null) container.readinessSignal = {timeoutSeconds: timeout};
        }

        versionAttr = requireAttribute(text, diagnostics, containerBlock, "version");
        version = stringValue(text, diagnostics, versionAttr, "Version");
    }

    const network = exactlyOneBlock(text, diagnostics, deployment, "network");
    const networking = {};
    if (network) {
        validateMembers(text, diagnostics, network, new Set(["mode", "ingress"]), new Set());
        const modeAttr = requireAttribute(text, diagnostics, network, "mode");
        const mode = stringValue(text, diagnostics, modeAttr, "Network mode");
        if (mode !== "virtual" && mode !== "host") {
            if (mode !== null) diagnostics.push(diagnostic(text, modeAttr.value, 'Network mode must be "virtual" or "host".'));
        } else {
            networking.mode = mode === "virtual" ? NETWORK_VIRTUAL : NETWORK_HOST;
        }
        const routeCount = parseIngress(text, diagnostics, firstAttribute(network, "ingress"), networking);
        if (mode === "host" && routeCount) diagnostics.push(diagnostic(text, network, "Host networking cannot contain ingress routes."));
    }

    const running = booleanValue(text, diagnostics, requireAttribute(text, diagnostics, deployment, "desired_running"), "desired_running");
    if (running && version !== null && !version) {
        diagnostics.push(diagnostic(text, versionAttr?.value || versionAttr, "Version cannot be empty while desired_running is true."));
    }
    if (sourceSpec.nixDockerBuild && version && !FULL_GIT_COMMIT_RE.test(version)) {
        diagnostics.push(diagnostic(text, versionAttr?.value || versionAttr, "Nix versions must be full 40-character commits."));
    }
    const explicitImageVersion = imageReferenceVersion(sourceSpec.remoteImage?.image);
    if (explicitImageVersion && version !== null && version !== explicitImageVersion) {
        diagnostics.push(diagnostic(text, versionAttr?.value || versionAttr, `Version must match ${quote(explicitImageVersion)} from the image reference.`));
    }
    const initialVersion = unwrap(constraints?.initialVersion);
    if (constraints?.updateMode && running === false && initialVersion !== undefined && initialVersion !== null
        && version !== null && version !== String(initialVersion)) {
        diagnostics.push(diagnostic(text, versionAttr?.value || versionAttr, "Version cannot change while an updated deployment is stopped."));
    }
    const immutableName = unwrap(constraints?.immutableName);
    if (immutableName !== undefined && immutableName !== null && name !== null && name !== String(immutableName)) {
        diagnostics.push(diagnostic(text, nameAttr?.value || nameAttr, `Deployment name is immutable and must remain ${quote(immutableName)}.`));
    }
    const immutableNodeId = unwrap(constraints?.immutableNodeId);
    if (immutableNodeId !== undefined && immutableNodeId !== null && nodeId !== null && Number(nodeId) !== Number(immutableNodeId)) {
        diagnostics.push(diagnostic(text, nodeAttr?.value || nodeAttr, "Deployment node placement is immutable."));
    }

    if (diagnostics.some(item => item.severity === "error")) return null;
    container.version = version;
    container.running = running;
    return {
        identity: {name, spaceId},
        nodeId,
        spec: {container1Spec: container, networking},
    };
}

export function parseDeploymentHcl(text, catalogs = {}, constraints = {}) {
    const source = String(text ?? "");
    const diagnostics = [];
    let ast;
    try {
        ast = new Parser(source).parse();
    } catch (error) {
        if (error instanceof ParseFailure) {
            diagnostics.push(diagnostic(source, error, error.message));
            return {document: null, diagnostics};
        }
        throw error;
    }
    const document = parseValidatedDocument(source, ast, normalizedCatalogs(catalogs), unwrap(constraints) || {}, diagnostics);
    return {document, diagnostics};
}
