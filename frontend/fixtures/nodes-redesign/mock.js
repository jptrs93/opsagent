// In-memory backend for the nodes fixture. The datasets are written straight
// into the real state stores (machinesS, enrollmentsS, backupStatusS, spacesS,
// systemConfigS, deploymentsS) and installMockApi() replaces the node
// endpoints on the generated capi object with functions that mutate those
// stores, so rename, node access policies, drain, evict and enrollment accept
// all work end to end and the tables update live. A node's allowedSpaces is
// derived from the policies, as the server derives it.
import {backupStatusS, deploymentsS, deploymentsStreamS, enrollmentsS, machinesS, nodeAccessPoliciesS, spacesS, systemConfigS, userConfigRefsS} from "/src/state/deployments.js";
import {denyEffect, exactNodes, runPermission} from "/src/lib/nodeAccessPolicies.js";
import {builtinPolicies, runSpaces, writePolicies} from "/fixtures/nodeAccess.js";

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const fail = (status, code, message) => Object.assign(new Error(message), {status, code});
const ago = (ms) => new Date(Date.now() - ms);
const MIN = 60e3, HOUR = 3600e3, DAY = 86400e3;

const KEYS = [
    "Qm3xTzpL9vK2wR8nY5sH1cJ7dF4gB6aE0uI2oP9lM3k=",
    "aH7kLm2PqR9sT4vW6xY1zB3cD5eF8gJ0nM2oQ4rU6i=",
    "zX9cV2bN4mK7jH5gF3dS1aQ8wE6rT4yU2iO0pL9kM7n=",
    "pL5kJ3hG1fD9sA7qW2eR4tY6uI8oZ0xC3vB5nM7mK9l=",
    "rT2yU4iO6pA8sD0fG1hJ3kL5zX7cV9bN2mQ4wE6rT8y=",
    "mN8bV6cX4zL2kJ0hG9fD7sA5qW3eR1tY4uI6oP8lK2j=",
    "eR6tY8uI0oP2aS4dF6gH8jK0lZ2xC4vB6nM8qW0eR2t=",
    "wE4rT6yU8iO0pA2sD4fG6hJ8kL0zX2cV4bN6mQ8wE0r=",
    "cV7bN9mQ1wE3rT5yU7iO9pA1sD3fG5hJ7kL9zX1cV3b=",
    "sD1fG3hJ5kL7zX9cV1bN3mQ5wE7rT9yU1iO3pA5sD7f=",
    "gH3jK5lZ7xC9vB1nM3qW5eR7tY9uI1oP3aS5dF7gH9j=",
    "kL9zX1cV3bN5mQ7wE9rT1yU3iO5pA7sD9fG1hJ3kL5z=",
    "uI5oP7aS9dF1gH3jK5lZ7xC9vB1nM3qW5eR7tY9uI1o=",
];

// A node as publishDerived() builds it: the view model plus the status overlay.
const node = (id, name, identifier, overrides = {}) => ({
    id, seq: 100 + id,
    name, identifier,
    roles: [], allowedSpaces: [],
    enrolledAt: ago(40 * DAY),
    status: 4, enrollmentRequestedAt: null,
    draining: false, evicted: false, eventTime: 0,
    addresses: [`fd7a:115c:a1e0::${id}`],
    hostAddresses: [`10.0.0.${10 + id}`], hostAddressesUnknown: false,
    wgPublicKey: KEYS[id % KEYS.length],
    isPrimary: false, connected: true, connectedAt: ago(3 * DAY),
    runtimeVersions: "containerd 2.0.4, runc 1.2.6", opendeployVersion: "v0.0.617",
    ...overrides,
});

const primary = (id, name, identifier, overrides = {}) =>
    node(id, name, identifier, {roles: [0], isPrimary: true, connectedAt: undefined, ...overrides});

const evictedNode = (id, name, identifier, when) =>
    node(id, name, identifier, {status: 8, evicted: true, eventTime: when.getTime(), connected: false, connectedAt: undefined, runtimeVersions: "", opendeployVersion: ""});

const enrollment = (id, machineId, ip, when, overrides = {}) => ({
    id, seq: 200 + id,
    createdAt: when,
    requestingMachineId: machineId, requestingIpAddress: ip,
    underlayAddress: "", hostAddresses: [ip],
    opendeployVersion: "v0.0.617", isConnected: true,
    status: 1,
    ...overrides,
});

const SPACES = [
    {id: 0, name: "_system"},
    {id: 1, name: "global"},
    {id: 2, name: "staging"},
    {id: 3, name: "prod"},
];

const BUSY_SPACES = [
    ...SPACES,
    {id: 4, name: "analytics"},
    {id: 5, name: "edge"},
    {id: 6, name: "sandbox"},
];

const literal = (value) => ({value: {value: {literal: value}}});
const systemConfig = () => ({
    settings: {
        cluster: {
            listen: literal("primary.example.internal:9443"),
            enrollmentListen: literal("primary.example.internal:9444"),
        },
    },
});

// The self deployment on the primary supplies the version in the install
// command, as the shipped page reads it.
const selfDeployment = (nodeId, version) => ({
    config: {
        deployment: {
            spaceId: 0, name: "opendeploy",
            spec: {workload: {value: {container: {version}}}},
            scheduling: {running: true, placement: {value: {dedicatedNodes: {nodes: [nodeId]}}}},
        },
    },
    status: {runner: {runningVersion: version}},
});

const userDeployment = (name, spaceId, nodeId) => ({
    config: {
        deployment: {
            spaceId, name,
            spec: {workload: {value: {container: {version: "1.4.2"}}}},
            scheduling: {running: true, placement: {value: {dedicatedNodes: {nodes: [nodeId]}}}},
        },
    },
    status: {},
});

export const DATASETS = {
    typical: () => ({
        spaces: SPACES,
        machines: [
            primary(1, "primary", "7c1f0a6e-primary-0001", {
                hostAddresses: ["10.0.0.11", "203.0.113.4", "2001:db8::11"],
                runtimeVersions: "containerd 2.0.4, runc 1.2.6",
            }),
            node(2, "worker-a", "a19b4f02-5ca3-4ad2-9b61-ee0d1c2a3b44", {connectedAt: ago(12 * DAY)}),
            node(3, "worker-b", "b7e2c813-1d4f-4a8e-a0c2-9f3b7d1e5c66", {
                connectedAt: ago(2 * HOUR),
                hostAddresses: ["10.0.0.13", "203.0.113.6"],
            }),
            node(4, "worker-c", "c4d9e1f0-8b2a-4c6d-b3e7-1a5f9c8d2e77", {
                draining: true, status: 6, connectedAt: ago(5 * DAY),
                runtimeVersions: "containerd 2.0.3, runc 1.2.5", opendeployVersion: "v0.0.616",
            }),
            node(5, "edge-1", "d8f3a7b2-6e1c-4f9a-8d2b-3c7e1f5a9b88", {
                connected: false, connectedAt: ago(9 * DAY), hostAddresses: ["198.51.100.20"],
            }),
            evictedNode(6, "old-worker", "e2a6c4d8-0f3b-4e7a-9c1d-5b8f2a6e4c99", ago(20 * DAY)),
        ],
        enrollments: [
            enrollment(7, "f5b1d9e3-2c7a-4b8f-a6e0-4d9c3b7f1a00", "10.0.0.17", ago(6 * MIN)),
        ],
        deployments: [
            selfDeployment(1, "v0.0.617"),
            userDeployment("api", 3, 2), userDeployment("worker", 3, 2), userDeployment("api", 2, 3),
        ],
        backup: {
            configured: true, running: true, inSync: true,
            localTxid: 184223, remoteTxid: 184223,
            lastSuccessfulSyncAt: ago(42e3),
            assetPending: 0, assetKeepLocal: false, assetTargetS3: true,
        },
    }),

    busy: () => ({
        spaces: BUSY_SPACES,
        machines: [
            primary(1, "primary", "7c1f0a6e-primary-0001", {
                hostAddresses: ["10.0.0.11", "203.0.113.4", "2001:db8::11", "2001:db8::1:11"],
            }),
            ...Array.from({length: 11}, (_, i) => {
                const id = i + 2;
                const names = ["worker-a", "worker-b", "worker-c", "worker-d", "worker-e", "worker-f", "gpu-1", "gpu-2", "edge-syd", "edge-akl", "edge-lon"];
                const extra = (n) => Array.from({length: n}, (_, k) => `203.0.113.${id * 4 + k}`);
                return node(id, names[i], `${(0x1000 + id * 7919).toString(16)}b2c4-${id}e7f-4a1c-9d3b-${String(id).padStart(12, "0")}`, {
                    hostAddresses: [`10.0.0.${10 + id}`, ...extra(i % 4)],
                    connected: i !== 6 && i !== 9,
                    draining: i === 4, status: i === 4 ? 6 : 4,
                    connectedAt: ago((i + 1) * 7 * HOUR),
                    runtimeVersions: i % 5 === 0 ? "containerd 2.0.3, runc 1.2.5" : "containerd 2.0.4, runc 1.2.6",
                    opendeployVersion: i % 5 === 0 ? "v0.0.616" : "v0.0.617",
                });
            }),
            evictedNode(20, "worker-old-1", "f1e2d3c4-b5a6-4978-8c9d-0e1f2a3b4c5d", ago(60 * DAY)),
            evictedNode(21, "worker-old-2", "a9b8c7d6-e5f4-4321-9876-543210fedcba", ago(33 * DAY)),
        ],
        enrollments: [
            enrollment(30, "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "10.0.0.40", ago(2 * MIN)),
            enrollment(31, "1b2c3d4e-5f6a-4b7c-9d8e-0f1a2b3c4d5e", "10.0.0.41", ago(25 * MIN), {opendeployVersion: "v0.0.616"}),
            enrollment(32, "2c3d4e5f-6a7b-4c8d-ae9f-1a2b3c4d5e6f", "198.51.100.77", ago(3 * HOUR)),
        ],
        deployments: [
            selfDeployment(1, "v0.0.617"),
            ...Array.from({length: 24}, (_, i) => userDeployment(`svc-${i}`, 1 + (i % 6), 2 + (i % 11))),
        ],
        backup: {
            configured: true, running: true, inSync: false,
            localTxid: 991204, remoteTxid: 990877,
            lastSuccessfulSyncAt: ago(7 * MIN),
            assetPending: 14, assetKeepLocal: false, assetTargetS3: true,
        },
    }),

    empty: () => ({
        spaces: SPACES.slice(0, 2),
        machines: [primary(1, "primary", "7c1f0a6e-primary-0001")],
        enrollments: [],
        deployments: [selfDeployment(1, "v0.0.617")],
        backup: null,
    }),

    trouble: () => ({
        spaces: SPACES,
        machines: [
            primary(1, "primary", "7c1f0a6e-primary-0001"),
            node(2, "worker-a", "a19b4f02-5ca3-4ad2-9b61-ee0d1c2a3b44", {connected: false, connectedAt: ago(3 * DAY)}),
            node(3, "worker-b", "b7e2c813-1d4f-4a8e-a0c2-9f3b7d1e5c66", {connected: false, connectedAt: ago(3 * DAY)}),
            node(4, "worker-c", "c4d9e1f0-8b2a-4c6d-b3e7-1a5f9c8d2e77", {draining: true, status: 6, runtimeVersions: "", opendeployVersion: "v0.0.611", hostAddresses: [], hostAddressesUnknown: true}),
        ],
        enrollments: [
            enrollment(8, "f5b1d9e3-2c7a-4b8f-a6e0-4d9c3b7f1a00", "10.0.0.18", ago(2 * DAY), {opendeployVersion: "v0.0.611"}),
        ],
        deployments: [selfDeployment(1, "v0.0.617"), userDeployment("api", 3, 2)],
        backup: {
            configured: true, running: false, inSync: false,
            localTxid: 184223, remoteTxid: 181900,
            lastSuccessfulSyncAt: ago(2 * DAY),
            assetPending: 3, assetKeepLocal: true, assetTargetS3: true,
            error: "dial tcp 10.7.20.100:5432: connect: connection refused",
            assetError: "s3 put backups/assets/9f3b7d1e: RequestTimeout after 30s",
        },
    }),
};

const onlySpaces = (id, name, nodeIds, spaceIds, disabled = false) => ({
    id, seq: 40 + id, name, disabled, builtin: 0, nodes: exactNodes(nodeIds),
    rule: {effect: denyEffect(), selector: {permissions: runPermission(), spaces: {allSpacesExcluding: {values: spaceIds}}}},
});

const POLICIES = {
    typical: () => [onlySpaces(3, "worker_b_is_staging_only", [3], [2]), onlySpaces(4, "edge_no_prod", [5], [1, 2]), onlySpaces(5, "worker_c_is_prod_only", [4], [3], true)],
    busy: () => [onlySpaces(3, "primary_runs_no_workloads", [1], []), onlySpaces(4, "edge_pool", [4, 7, 10], [1, 5, 6], true)],
};

const withAccess = (machines) => {
    const spaceIds = spacesS.val.map((s) => Number(s.id));
    return machines.map((m) => ({...m, allowedSpaces: m.evicted ? [] : runSpaces(nodeAccessPoliciesS.val, m.id, spaceIds)}));
};

export function populate(name) {
    const data = (DATASETS[name] || DATASETS.typical)();
    spacesS.val = data.spaces;
    nodeAccessPoliciesS.val = [...builtinPolicies().map((p) => ({...p, seq: 1})), ...(POLICIES[name] || (() => []))()];
    machinesS.val = withAccess(data.machines);
    enrollmentsS.val = data.enrollments;
    deploymentsS.val = data.deployments;
    backupStatusS.val = data.backup;
    systemConfigS.val = systemConfig();
    userConfigRefsS.val = [];
    deploymentsStreamS.val = {status: "connected", sentence: "connected", lastError: ""};
    return data;
}

const nextNodeId = () => [...machinesS.val, ...enrollmentsS.val].reduce((max, item) => Math.max(max, Number(item.id) || 0), 0) + 1;

export function installMockApi(capi, {delayMs = 300} = {}) {
    const find = (identifier) => {
        const machine = machinesS.val.find((m) => m.identifier === identifier);
        if (!machine) throw fail(404, "not_found", "no such node");
        return machine;
    };
    const replace = (machine, patch) => {
        const next = {...machine, ...patch, seq: Number(machine.seq || 0) + 1};
        machinesS.val = machinesS.val.map((m) => m === machine ? next : m);
        return next;
    };
    let policySeq = 100;
    const refuse = (status, displayErr) => Object.assign(new Error(displayErr), {status, apiErr: {displayErr}});
    const writeNodeAccess = (write) => {
        const spaceNames = new Map(spacesS.val.map((s) => [Number(s.id), s.name]));
        const placements = deploymentsS.val.map(({config}) => {
            const nodeId = Number(config.deployment.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || 0);
            const spaceId = Number(config.deployment.spaceId || 0);
            return {deployment: config.deployment.name, spaceId, nodeId, space: spaceNames.get(spaceId), node: machinesS.val.find((m) => m.id === nodeId)?.name};
        }).filter((d) => d.nodeId);
        nodeAccessPoliciesS.val = writePolicies(nodeAccessPoliciesS.val, write, placements, refuse).sort((a, b) => a.id - b.id);
        machinesS.val = withAccess(machinesS.val);
    };
    Object.assign(capi, {
        // The login state starts the real event stream; a stream that never
        // yields keeps it quiet instead of retrying against the dev server.
        postV1GlobalEventStream() {
            return (async function* () { await new Promise(() => {}); })();
        },
        async getV1NodesEnrollmentsInfo() {
            await delay(delayMs);
            return {enrollmentTlsSpkiSha256: "sha256:4f2a9c1e7b3d5a8f0c6e2b9d1a7f3c5e8b0d2f4a6c8e0b1d3f5a7c9e1b3d5f7a"};
        },
        async postV1NodesRename({identifier, name}) {
            await delay(delayMs);
            if (!name?.trim()) throw fail(400, "invalid_name", "a node needs a name");
            if (machinesS.val.some((m) => m.identifier !== identifier && !m.evicted && m.name === name)) {
                throw fail(409, "duplicate_name", `a node named ${name} already exists`);
            }
            return replace(find(identifier), {name});
        },
        async postV1NodeAccessPoliciesCreate({name, disabled, nodes, rule}) {
            await delay(delayMs);
            const id = nodeAccessPoliciesS.val.reduce((max, p) => Math.max(max, p.id), 0) + 1;
            writeNodeAccess({op: "create", id, policy: {id, seq: ++policySeq, name, disabled: Boolean(disabled), builtin: 0, nodes, rule}});
            return {};
        },
        async postV1NodeAccessPoliciesUpdate({id, expectedSeq, name, disabled, nodes, rule}) {
            await delay(delayMs);
            const current = nodeAccessPoliciesS.val.find((p) => p.id === Number(id));
            if (current && expectedSeq && Number(expectedSeq) !== current.seq) throw refuse(409, "Node access policy was modified concurrently");
            writeNodeAccess({op: "update", id: Number(id), policy: {id: Number(id), seq: ++policySeq, name, disabled: Boolean(disabled), nodes, rule}});
            return {};
        },
        async postV1NodeAccessPoliciesDelete({id}) {
            await delay(delayMs);
            writeNodeAccess({op: "delete", id: Number(id)});
        },
        async postV1NodesDrain({identifier, draining}) {
            await delay(delayMs);
            const machine = find(identifier);
            if (machine.isPrimary) throw fail(409, "primary", "the primary cannot be drained");
            return replace(machine, {draining: Boolean(draining), status: draining ? 6 : 4});
        },
        async postV1NodesExposure({identifier}) {
            await delay(delayMs);
            const machine = find(identifier);
            const held = deploymentsS.val.filter((row) => Number(row.config?.deployment?.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0]) === machine.id);
            return {
                deployments: held.map((row, i) => ({id: 100 + i, name: row.config.deployment.name, version: 3})),
                secrets: held.length ? [{id: 7, name: "db_password", version: 2}] : [],
                configs: [],
                issuedTlsDeployments: [],
                acmeHostnames: [],
                githubToken: held.length > 0,
            };
        },
        async postV1NodesEvict({identifier, expectedSeq, force}) {
            await delay(delayMs);
            const machine = find(identifier);
            if (machine.isPrimary) throw fail(409, "primary", "the primary cannot be evicted");
            if (Number(expectedSeq) !== Number(machine.seq)) throw fail(409, "stale", "the node changed; reload and try again");
            const pinned = deploymentsS.val.filter((row) => Number(row.config?.deployment?.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0]) === machine.id
                && Number(row.config?.deployment?.spaceId) !== 0);
            if (pinned.length && !force) throw fail(409, "pinned", "deployments are still placed on this node");
            replace(machine, {status: 8, evicted: true, draining: false, connected: false, connectedAt: undefined, runtimeVersions: "", eventTime: Date.now()});
            return {};
        },
        async postV1NodesEnrollmentsAccept({id, nodeName, expectedSeq}) {
            await delay(delayMs);
            const req = enrollmentsS.val.find((e) => Number(e.id) === Number(id));
            if (!req) throw fail(404, "not_found", "no such enrollment request");
            if (Number(expectedSeq) !== Number(req.seq)) throw fail(409, "stale", "the request changed; reload and try again");
            if (!nodeName?.trim()) throw fail(400, "invalid_name", "a node needs a name");
            if (machinesS.val.some((m) => !m.evicted && m.name === nodeName)) throw fail(409, "duplicate_name", `a node named ${nodeName} already exists`);
            enrollmentsS.val = enrollmentsS.val.filter((e) => e !== req);
            machinesS.val = withAccess([...machinesS.val, node(req.id || nextNodeId(), nodeName, req.requestingMachineId, {
                enrolledAt: new Date(), connectedAt: new Date(), opendeployVersion: req.opendeployVersion,
                addresses: [`fd7a:115c:a1e0::${req.id}`], hostAddresses: req.hostAddresses,
            })]);
            return {};
        },
    });
}
