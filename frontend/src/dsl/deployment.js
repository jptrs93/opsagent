import {compileSchema} from "./schema.js";
import {defineFunctions} from "./functions.js";
import {evaluate} from "./evaluate.js";
import {print} from "./print.js";
import {attribute, raw} from "./print.js";
import {formatIpPrefix, parseIpPrefix} from "../lib/ipaddr.js";
import {imageReference, imageRepositoryFromReference, imageVersionFromReference} from "../components/deploymentSource.js";

export const GLOBAL_SPACE_ID = 1;

export const deploymentTypeDefinition = `type Deployment root {
    id: u64
    name: string
    space_id: Space.id
    spec: type DeploymentSpec {
        workload: type ContainerSpec {
            source: type NixImageBuild {
                repo: string
                flake: string
                target: string
            } | type RemoteImage {
                image: string
            }
            runtime: type ContainerRuntime {
                user: string
                env_vars: map<string, type LiteralEnv {
                    value: string
                } | type SecretEnv {
                    secret: SecretRef
                } | type ConfigEnv {
                    config: ConfigRef
                } | type AssetEnv {
                    key: string
                    asset: AssetRef
                } | type AddressEnv {
                    deployment_id: Deployment.id
                    space_id: Space.id
                }>
                override_command: []string
                override_working_dir: string
                default_volume: type DefaultVolumeMount {
                    container_path: string
                    disabled: bool
                }
                cross_deployment_mounts: []type CrossDeploymentMount {
                    deployment_id: Deployment.id
                    container_path: string
                    permission: FilePermission
                }
                mounts: []type HostMount {
                    host_path: string
                    container_path: string
                    permission: FilePermission
                }
                asset_mounts: []type AssetMount {
                    asset: AssetRef
                    container_path: string
                    permission: FilePermission
                }
                dev_shm_size_kb: ?i32
                file_descriptor_limit: ?i32
                issued_tls_mount: ?type IssuedTLSMount {
                    container_path: string
                    extra_names: []string
                    ca_only: bool
                }
            }
            version: string
            upgrade_strategy: enum ContainerUpgradeStrategy {
                Recreate
                Rollover
            }
            readiness_signal: ?type ContainerReadinessSignal {
                timeout_seconds: ?i32
            }
        }
        networking: type NetworkingConfig {
            mode: enum NetworkingMode {
                Virtual
                Host
            }
            port_forwarding: []type PortForward {
                protocol: enum PortForwardProtocol {
                    Tcp
                    Udp
                }
                host_port: i32
                container_port: i32
                ip_filter: []type IpFilter {
                    mode: enum IpFilterMode {
                        Allow
                        Deny
                    }
                    prefix: IPv4Prefix | IPv6Prefix
                }
            }
            ingress: []type Ingress {
                hostname: string
                listen: []type IngressListen {
                    node: ?(type AnyNode {
                    } | type SpecificNode {
                        node_id: Node.id
                    })
                    addresses: [](IPv4Prefix | IPv6Prefix)
                }
                config: type TlsPassthroughConfig {
                    host_port: ?i32
                    container_port: i32
                } | type HttpsConfig {
                    container_port: i32
                    path_prefix: string
                    strip_prefix: bool
                    backend_protocol: enum HttpBackendProtocol {
                        Http1
                        H2c
                    }
                    max_request_body_bytes: ?i64
                    flush_interval_ms: ?i32
                    cert_source: ?(type AcmeCertSource {
                        challenge: enum AcmeChallenge {
                            Http01
                        }
                    } | type SecretCertSource {
                        secret: SecretRef
                    })
                }
            }
        }
    }
    scheduling: type Scheduling {
        running: bool
        placement: type DedicatedNodesScheduling {
            nodes: []Node.id
        }
        restart_generation: u32
    }
}

enum FilePermission {
    ReadWrite
    ReadOnly
    ReadExecute
}

type IPv4Address {
    octets: [4]u8
}

type IPv6Address {
    octets: [16]u8
}

type IPv4Prefix {
    address: IPv4Address
    prefix_length: u8
}

type IPv6Prefix {
    address: IPv6Address
    prefix_length: u8
}

type SecretRef {
    secret_id: Secret.id
    version: u32
}

type ConfigRef {
    config_id: Config.id
    version: u32
}

type AssetRef {
    asset_id: Asset.id
    version: u32
}

// Roots the deployment refers to, trimmed to what a reference needs.
// The full model of each lives in the e2edesign repository.

type Space root {
    id: u64
    name: string
}

type Node root {
    id: u64
    name: string
}

type Secret root {
    id: u64
}

type Config root {
    id: u64
}

type Asset root {
    id: u64
}
`;

let compiled = null;

export function deploymentSchema() {
    if (!compiled) {
        const {schema, diagnostics} = compileSchema(deploymentTypeDefinition);
        if (!schema) throw new Error(`Deployment type definition does not compile: ${diagnostics.map(item => item.message).join(" ")}`);
        compiled = schema;
    }
    return compiled;
}

const quote = value => JSON.stringify(String(value));
const capitalize = word => `${word[0].toUpperCase()}${word.slice(1)}`;

function normalizeCatalog(catalog = {}) {
    const list = items => (items || []).map(item => ({...item, id: Number(item.id)}));
    return {
        spaces: list(catalog.spaces),
        nodes: list(catalog.nodes),
        deployments: list(catalog.deployments).map(item => ({...item, spaceId: Number(item.spaceId)})),
        secrets: list(catalog.secrets).map(item => ({...item, spaceId: Number(item.spaceId), version: Number(item.version)})),
        configs: list(catalog.configs).map(item => ({...item, spaceId: Number(item.spaceId), version: Number(item.version)})),
        assets: list(catalog.assets).map(item => ({...item, spaceId: Number(item.spaceId), version: Number(item.version)})),
    };
}

const prefixInstance = parsed => parsed.value.ipv4
    ? {ipv4_prefix: {address: {octets: Array.from(parsed.value.ipv4.address.octets)}, prefix_length: parsed.value.ipv4.prefixLength}}
    : {ipv6_prefix: {address: {octets: Array.from(parsed.value.ipv6.address.octets)}, prefix_length: parsed.value.ipv6.prefixLength}};

const ANY_IPV4 = prefixInstance(parseIpPrefix("0.0.0.0/0")).ipv4_prefix;
const ANY_IPV6 = prefixInstance(parseIpPrefix("::/0")).ipv6_prefix;

export function deploymentFunctions(catalog) {
    const refs = normalizeCatalog(catalog);
    const schema = deploymentSchema();

    const unique = (matches, label, name, ctx, where) => {
        if (matches.length === 1) return matches[0];
        return ctx.report(matches.length ? `${capitalize(label)} reference ${quote(name)} is ambiguous.` : `No ${label} named ${quote(name)} exists.`, where);
    };

    const resolveSpace = (name, ctx, where) => unique(refs.spaces.filter(space => space.name === name), "space", name, ctx, where);

    const local = (space, label, ctx, where) => {
        const own = ctx.root?.space_id;
        if (own !== undefined && space.id !== own && space.id !== GLOBAL_SPACE_ID) {
            return ctx.report(`${capitalize(label)} references may only use the deployment's space or the global space.`, where);
        }
        return space;
    };

    const versioned = (items, label, idField) => ([spaceName, path, version], ctx) => {
        const space = resolveSpace(spaceName, ctx, ctx.args[0]);
        if (!space || !local(space, label, ctx, ctx.args[0])) return null;
        if (version !== undefined && version < 1) return ctx.report(`${capitalize(label)} version must be a positive integer.`, ctx.args[2]);
        let matches = items.filter(item => item.spaceId === space.id && item.path === path);
        if (version !== undefined) {
            matches = matches.filter(item => item.version === version);
        } else {
            const latest = Math.max(-1, ...matches.map(item => item.version));
            matches = matches.filter(item => item.version === latest);
        }
        if (!matches.length) {
            return ctx.report(`No ${label} at ${quote(path)}${version !== undefined ? ` version ${version}` : ""} exists in space ${quote(spaceName)}.`);
        }
        return {[idField]: matches[0].id, version: matches[0].version};
    };

    const deploymentIn = ([spaceName, name], ctx) => {
        const space = resolveSpace(spaceName, ctx, ctx.args[0]);
        if (!space) return null;
        const match = unique(refs.deployments.filter(item => item.spaceId === space.id && item.name === name), "deployment", name, ctx, ctx.args[1]);
        return match ? {space, match} : null;
    };

    return defineFunctions(schema, {
        space: {
            signature: "(name: string) -> Space.id",
            impl: ([name], ctx) => resolveSpace(name, ctx, ctx.args[0])?.id ?? null,
        },
        node: {
            signature: "(name: string) -> Node.id",
            impl: ([name], ctx) => unique(refs.nodes.filter(item => item.name === name), "node", name, ctx, ctx.args[0])?.id ?? null,
        },
        deployment: {
            signature: "(space: string, name: string) -> Deployment.id",
            impl: (args, ctx) => deploymentIn(args, ctx)?.match.id ?? null,
        },
        address: {
            signature: "(space: string, deployment: string) -> AddressEnv",
            impl: (args, ctx) => {
                const found = deploymentIn(args, ctx);
                return found ? {deployment_id: found.match.id, space_id: found.space.id} : null;
            },
        },
        secret: {signature: "(space: string, path: string, version: ?u32) -> SecretRef", impl: versioned(refs.secrets, "secret", "secret_id")},
        config: {signature: "(space: string, path: string, version: ?u32) -> ConfigRef", impl: versioned(refs.configs, "config", "config_id")},
        asset: {signature: "(space: string, path: string, version: ?u32) -> AssetRef", impl: versioned(refs.assets, "asset", "asset_id")},
        acme: {signature: "() -> AcmeCertSource", impl: () => ({challenge: "Http01"})},
        any_node: {signature: "() -> AnyNode", impl: () => ({})},
        prefix: {
            signature: "(cidr: string) -> IPv4Prefix | IPv6Prefix",
            impl: ([cidr], ctx) => {
                const parsed = parseIpPrefix(cidr);
                return parsed ? prefixInstance(parsed) : ctx.report(`${quote(cidr)} is not an IPv4 or IPv6 prefix such as "203.0.113.0/24".`, ctx.args[0]);
            },
        },
        ipv4: {signature: "() -> IPv4Prefix", impl: () => structuredClone(ANY_IPV4)},
        ipv6: {signature: "() -> IPv6Prefix", impl: () => structuredClone(ANY_IPV6)},
    });
}

export function deploymentPrinters(catalog) {
    const refs = normalizeCatalog(catalog);
    const spaceName = id => refs.spaces.find(space => space.id === id)?.name ?? `space-${id}`;
    const deploymentName = id => refs.deployments.find(item => item.id === id)?.name ?? `deployment-${id}`;
    const versioned = (fn, items, idField) => value => {
        const rows = items.filter(item => item.id === value[idField]);
        const row = rows.find(item => item.version === value.version) || rows[0];
        const latest = Math.max(-1, ...rows.map(item => item.version));
        const pin = row && value.version !== latest ? `, ${value.version}` : "";
        return `${fn}(${quote(row ? spaceName(row.spaceId) : "?")}, ${quote(row?.path ?? `${fn}-${value[idField]}`)}${pin})`;
    };
    const prefix = family => value => {
        const any = family === "ipv4" ? ANY_IPV4 : ANY_IPV6;
        if (value.prefix_length === 0 && value.address.octets.every(octet => octet === 0)) return `${family}()`;
        const text = formatIpPrefix({value: {[family]: {address: {octets: value.address.octets}, prefixLength: value.prefix_length}}});
        return `prefix(${quote(text || formatIpPrefix({value: {[family]: {address: {octets: any.address.octets}, prefixLength: 0}}}))})`;
    };
    return {
        "Space.id": id => `space(${quote(spaceName(id))})`,
        "Node.id": id => `node(${quote(refs.nodes.find(node => node.id === id)?.name ?? `node-${id}`)})`,
        "Deployment.id": id => {
            const row = refs.deployments.find(item => item.id === id);
            return `deployment(${quote(row ? spaceName(row.spaceId) : "?")}, ${quote(deploymentName(id))})`;
        },
        SecretRef: versioned("secret", refs.secrets, "secret_id"),
        ConfigRef: versioned("config", refs.configs, "config_id"),
        AssetRef: versioned("asset", refs.assets, "asset_id"),
        AddressEnv: value => `address(${quote(spaceName(value.space_id))}, ${quote(deploymentName(value.deployment_id))})`,
        AcmeCertSource: () => "acme()",
        AnyNode: () => "any_node()",
        IPv4Prefix: prefix("ipv4"),
        IPv6Prefix: prefix("ipv6"),
    };
}

const PORT = [1, 65535];

const VERSION_HINT = 'version is written inside the source block: the tag of image = "repository:tag", or nix_image_build { version = "…" }.';
const IMAGE_VERSION_HINT = 'A container image is versioned by its reference: image = "repository:tag" or "repository@sha256:…"; there is no separate version attribute.';

const childBlock = (parent, name) => parent?.body?.find(item => item.kind === "block" && item.name === name);
const childAttribute = (parent, name) => parent?.body?.find(item => item.kind === "attribute" && item.name === name);
const without = (parent, item) => { parent.body = parent.body.filter(other => other !== item); };

export const deploymentCodeForm = {
    fromCode(ast, report) {
        const workload = childBlock(childBlock(ast.body.find(item => item.kind === "block" && item.name === "deployment"), "spec"), "workload");
        if (!workload) return ast;
        const stray = childAttribute(workload, "version");
        if (stray) {
            report(stray.nameToken, VERSION_HINT);
            without(workload, stray);
        }
        const source = childBlock(workload, "source");
        const remote = childBlock(source, "remote_image");
        const nix = childBlock(source, "nix_image_build");
        const sourceAttribute = childAttribute(workload, "source");
        const image = childAttribute(remote, "image") || (sourceAttribute?.value.kind === "string" ? sourceAttribute : null);
        const imageVersion = childAttribute(remote, "version");
        if (imageVersion) {
            report(imageVersion.nameToken, IMAGE_VERSION_HINT);
            without(remote, imageVersion);
        }
        if (image?.value.kind === "string") {
            const version = imageVersionFromReference(image.value.value);
            image.value = {...image.value, value: imageRepositoryFromReference(image.value.value)};
            workload.body.push({kind: "attribute", name: "version", nameToken: image.nameToken, value: {kind: "string", value: version, from: image.value.from, to: image.value.to}, from: image.from, to: image.to});
        }
        const nixVersion = childAttribute(nix, "version");
        if (nixVersion) {
            without(nix, nixVersion);
            workload.body.push(nixVersion);
        }
        return ast;
    },
    toCode(tree) {
        const workload = childBlock(childBlock(tree, "spec"), "workload");
        if (!workload) return tree;
        const versionItem = childAttribute(workload, "version");
        const version = versionItem?.value.kind === "string" ? versionItem.value.value : "";
        if (versionItem) without(workload, versionItem);
        const source = childBlock(workload, "source");
        const remote = childBlock(source, "remote_image");
        const nix = childBlock(source, "nix_image_build");
        const image = childAttribute(remote, "image");
        if (image && image.value.kind === "string") image.value = {kind: "string", value: imageReference(image.value.value, version)};
        if (nix && version) nix.body.push(attribute("version", {kind: "string", value: version}));
        return tree;
    },
};

export function deploymentRules(instance, report) {
    const workload = instance.spec?.workload;
    const runtime = workload?.runtime;
    const networking = instance.spec?.networking;
    const scheduling = instance.scheduling;
    const atLeast = (owner, field, min, label = field) => {
        const value = owner?.[field];
        if (value !== undefined && value !== null && value < min) report(owner, `${label} must be at least ${min}.`);
    };
    const inRange = (owner, field, [min, max]) => {
        const value = owner?.[field];
        if (value !== undefined && (value < min || value > max)) report(owner, `${field} must be between ${min} and ${max}.`);
    };

    if (!instance.name) report(instance, "name cannot be empty.");
    if (networking?.mode === "Host" && networking.port_forwarding?.length) report(networking, "Host networking cannot declare port_forwarding.");
    if (networking?.mode === "Host" && networking.ingress?.length) report(networking, "Host networking cannot declare ingress.");
    if (scheduling?.running && workload && !workload.version) {
        report(workload.source, workload.source?.nix_image_build
            ? "nix_image_build needs a version, the full 40-character commit sha, while running is true."
            : 'Container image must include a tag or digest (image = "repository:tag") while running is true.');
    }
    if (workload?.source?.nix_image_build && workload.version && !/^[0-9a-f]{40}$/.test(workload.version)) {
        report(workload.source, "A Nix image build version is the full 40-character commit sha.");
    }
    if (workload?.upgrade_strategy === "Rollover" && !workload.readiness_signal) report(workload, "The Rollover strategy needs a readiness_signal block.");
    atLeast(workload?.readiness_signal, "timeout_seconds", 1);
    atLeast(runtime, "dev_shm_size_kb", 1);
    atLeast(runtime, "file_descriptor_limit", 1);
    for (const forward of networking?.port_forwarding || []) {
        inRange(forward, "host_port", PORT);
        inRange(forward, "container_port", PORT);
        for (const filter of forward.ip_filter || []) {
            const v4 = filter.prefix?.ipv4_prefix;
            if (v4 && v4.prefix_length > 32) report(filter, "An IPv4 prefix length is at most 32.");
        }
    }
    for (const ingress of networking?.ingress || []) {
        if (!ingress.hostname) report(ingress, "hostname cannot be empty.");
        const https = ingress.config?.https_config;
        const passthrough = ingress.config?.tls_passthrough_config;
        inRange(https || passthrough, "container_port", PORT);
        if (passthrough?.host_port !== undefined) inRange(passthrough, "host_port", PORT);
        atLeast(https, "max_request_body_bytes", 1);
        atLeast(https, "flush_interval_ms", 1);
    }
    const nodes = scheduling?.placement?.nodes || [];
    if (nodes.length !== 1) report(scheduling?.placement || scheduling || instance, "placement must name exactly one node.");
}

export function evaluateDeployment(text, catalog) {
    const result = evaluate(deploymentSchema(), "Deployment", text, {functions: deploymentFunctions(catalog), rewrite: deploymentCodeForm.fromCode});
    if (!result.instance) return result;
    const before = result.diagnostics.length;
    deploymentRules(result.instance, (value, message) => {
        const node = result.nodeOf(value) || result.nodeOf(result.instance);
        result.diagnostics.push({from: node?.from ?? 0, to: node?.to ?? 0, severity: "error", message});
    });
    if (result.diagnostics.length > before) result.instance = null;
    return result;
}

export function printDeployment(instance, catalog) {
    return print(deploymentSchema(), "Deployment", instance, {printers: deploymentPrinters(catalog), rewrite: deploymentCodeForm.toCode});
}
