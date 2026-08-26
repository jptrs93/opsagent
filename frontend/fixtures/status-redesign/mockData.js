// Mock cluster for the status page redesign fixture. Instances are the unit of
// truth: a deployment carries one or more, each with its own node, runner
// status, version, and prepare phase, so the aggregated column treatments have
// mixed groups to chew on.

const day = 24 * 3600e3;
const hour = 3600e3;
const minute = 60e3;
const NOW = new Date("2026-08-26T14:20:00Z").getTime();

export const spaces = [
    {id: 1, name: "production"},
    {id: 2, name: "staging"},
    {id: 3, name: "default"},
    // The internal system space; deselected by default in the spaces filter.
    {id: 0, name: "_system"},
];

// status: 'running' | 'starting' | 'preparing' | 'stopped' | 'crashed' | 'prepare failed'
// prepare: label from preparerPhase() phrasing, tone 'ready' | 'progress' | 'failed'
const inst = (instanceId, node, status, version, prepare, restarts = 0, lastRestartAgoH = 0) => ({
    instanceId,
    node,
    status,
    version,
    prepare,
    restarts,
    lastRestartAt: restarts > 0 ? new Date(NOW - lastRestartAgoH * hour) : null,
});

const ready = {label: "ready", tone: "ready"};
const building = {label: "building image", tone: "progress"};
const resolving = {label: "resolving inputs", tone: "progress"};
const pulling = {label: "pulling image", tone: "progress"};
const imageFailed = {label: "image failed", tone: "failed"};

const dep = (id, name, spaceId, extras) => ({
    id,
    name,
    spaceId,
    variant: "nixDockerBuild",
    repo: `github.com/acme/${name}`,
    deployedBy: "joss",
    createdAt: new Date(NOW - (30 + id) * day),
    deployedAt: new Date(NOW - id * 5 * hour),
    inbound: {
        addr: `fd42:9a1c:0:${spaceId}::${(0x10 + id).toString(16)}`,
        dns: `${name.replaceAll("_", "-")}.space-${spaceId}.internal`,
    },
    ...extras,
});

export const deployments = [
    // production ------------------------------------------------------------
    dep(1, "api", 1, {
        desiredVersion: "e4f1c2a9d3b8",
        instances: [
            inst(101, "primary", "running", "e4f1c2a9d3b8", ready),
            inst(102, "worker-a", "running", "e4f1c2a9d3b8", ready),
            inst(103, "worker-b", "running", "e4f1c2a9d3b8", ready),
        ],
    }),
    dep(2, "web", 1, {
        // Mid-rollover: old instance serving, replacement still building.
        desiredVersion: "9c07ad5e21f6",
        deployedBy: "alex",
        instances: [
            inst(104, "primary", "running", "b7d92e1f44aa", ready),
            inst(105, "primary", "preparing", "9c07ad5e21f6", building),
        ],
    }),
    dep(3, "ingest", 1, {
        desiredVersion: "77aefc03d912",
        instances: [
            inst(106, "worker-a", "running", "77aefc03d912", ready, 1, 26),
            inst(107, "worker-b", "running", "77aefc03d912", ready),
            inst(108, "primary", "crashed", "77aefc03d912", ready, 4, 0.4),
        ],
    }),
    dep(4, "billing", 1, {
        variant: "githubRelease",
        desiredVersion: "v0.3.12",
        deployedBy: "alex",
        instances: [inst(109, "primary", "running", "v0.3.12", ready)],
    }),
    dep(5, "postgres", 1, {
        variant: "containerImage",
        repo: "docker.io/library/postgres",
        desiredVersion: "16.4-bookworm",
        instances: [inst(110, "worker-a", "running", "16.4-bookworm", ready, 1, 240)],
    }),
    dep(6, "scheduler", 1, {
        desiredVersion: "51b3e8d0c47a",
        instances: [inst(111, "primary", "running", "51b3e8d0c47a", ready)],
    }),

    // staging ---------------------------------------------------------------
    dep(7, "api", 2, {
        desiredVersion: "f00dbead1234",
        instances: [inst(112, "worker-a", "running", "f00dbead1234", ready)],
    }),
    dep(8, "web", 2, {
        desiredVersion: "0aa917cc3e55",
        deployedBy: "alex",
        instances: [inst(113, "worker-a", "starting", "0aa917cc3e55", ready)],
    }),
    dep(9, "mailer", 2, {
        desiredVersion: "3d3d90bb61c0",
        instances: [inst(114, "worker-b", "prepare failed", "3d3d90bb61c0", imageFailed)],
    }),
    dep(10, "search", 2, {
        desiredVersion: "8e442af7b19d",
        instances: [
            inst(115, "worker-a", "running", "8e442af7b19d", ready),
            inst(116, "worker-b", "running", "8e442af7b19d", resolving),
        ],
    }),

    // default ---------------------------------------------------------------
    dep(11, "batch-backfill", 3, {
        desiredVersion: "6b1c0d9e2f38",
        instances: [inst(117, "worker-b", "stopped", "6b1c0d9e2f38", ready)],
    }),
    dep(12, "playground", 3, {
        variant: "containerImage",
        repo: "docker.io/bitnami/kibana",
        desiredVersion: "2025.7.23-debian-12-r5",
        deployedBy: "alex",
        instances: [inst(118, "primary", "running", "2025.7.23-debian-12-r5", pulling)],
    }),
    dep(13, "metrics-agent", 3, {
        desiredVersion: "acdc19288bef",
        instances: [
            inst(119, "primary", "running", "acdc19288bef", ready),
            inst(120, "worker-a", "running", "acdc19288bef", ready),
            inst(121, "worker-b", "starting", "acdc19288bef", ready),
        ],
    }),

    // _system ---------------------------------------------------------------
    dep(14, "opendeploy-net", 0, {
        variant: "githubRelease",
        repo: "github.com/jptrs93/opsagent",
        desiredVersion: "v0.0.507",
        instances: [
            inst(122, "primary", "running", "v0.0.507", ready),
            inst(123, "worker-a", "running", "v0.0.507", ready),
            inst(124, "worker-b", "running", "v0.0.507", ready),
        ],
    }),
    dep(15, "opendeploy", 0, {
        variant: "githubRelease",
        repo: "github.com/jptrs93/opsagent",
        desiredVersion: "v0.0.507",
        instances: [
            inst(125, "primary", "running", "v0.0.507", ready),
            inst(126, "worker-a", "running", "v0.0.507", ready),
            inst(127, "worker-b", "running", "v0.0.506", ready),
        ],
    }),
];

// ---------------------------------------------------------------------------
// History. Deterministic per deployment: created → prepare → run, then a few
// version bumps, then whatever the deployment's current instances imply
// (crash storms, a stop, a failed image). Newest first, like the API.
// ---------------------------------------------------------------------------

const shortSha = (v) => (v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.slice(0, 7) : v);
const fakeSha = (id, i) => ((id * 2654435761 + i * 40503) >>> 0).toString(16).padStart(8, "0").repeat(2).slice(0, 12);

export function historyFor(deployment) {
    const entries = [];
    let t = deployment.createdAt.getTime();
    let v = 1;
    const by = deployment.deployedBy;
    const push = (offsetMs, kind, version, change, author = "") => {
        t += offsetMs;
        entries.push({at: new Date(t), kind, v: version, change, by: author, targetVersion: null});
    };

    push(0, "config", v, "created", by);
    push(2 * minute, "status", null, "prepare: preparing inputs=resolving");
    push(3 * minute, "status", null, "prepare: ready inputs=ready image=ready");
    push(1 * minute, "status", null, "run: running pid=2841 restarts=0");

    const bumps = 2 + (deployment.id % 3);
    for (let i = 0; i < bumps; i++) {
        const sha = i === bumps - 1 ? deployment.desiredVersion : fakeSha(deployment.id, i);
        v++;
        push((20 + deployment.id + i * 7) * hour, "config", v, `version=${shortSha(sha)}`, i % 2 === 0 ? by : "alex");
        entries[entries.length - 1].targetVersion = sha;
        push(1 * minute, "status", null, "prepare: preparing image=building");
        push((3 + i) * minute, "status", null, "prepare: ready image=ready");
        push(40e3, "status", null, `run: running pid=${3100 + deployment.id * 13 + i} restarts=0`);
    }

    const statuses = deployment.instances.map((instance) => instance.status);
    if (statuses.includes("crashed")) {
        push(9 * hour, "status", null, "run: crashed pid=0 restarts=3 last_restart=13:42");
        push(6 * minute, "status", null, "run: starting pid=4210 restarts=4");
        push(2 * minute, "status", null, "run: crashed pid=0 restarts=4 last_restart=13:50");
    }
    if (statuses.includes("stopped")) {
        v++;
        push(14 * hour, "config", v, "running=false", by);
        push(30e3, "status", null, "run: stopped pid=0 restarts=0");
    }
    if (statuses.includes("prepare failed")) {
        v++;
        push(5 * hour, "config", v, `version=${shortSha(deployment.desiredVersion)}`, by);
        entries[entries.length - 1].targetVersion = deployment.desiredVersion;
        push(1 * minute, "status", null, "prepare: preparing image=building");
        push(4 * minute, "status", null, "prepare: failed image=failed");
    }
    if (statuses.includes("preparing")) {
        push(2 * hour, "status", null, "prepare: preparing image=building");
    }

    entries.reverse();
    const latestConfigV = Math.max(...entries.filter((e) => e.kind === "config").map((e) => e.v));
    for (const e of entries) e.latestConfig = e.kind === "config" && e.v === latestConfigV;
    return entries;
}
