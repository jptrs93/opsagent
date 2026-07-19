const version = (id, label, day) => ({
    id,
    label,
    author: 'fixture@example.com',
    time: new Date(`2026-07-${String(day).padStart(2, '0')}T12:00:00Z`),
});

export const mockNodes = [
    {id: 11, identifier: 'edge-lon-1', name: 'London edge'},
    {id: 12, identifier: 'core-fra-1', name: 'Frankfurt core'},
];

export const mockSpaces = [
    {id: 0, name: 'opendeploy'},
    {id: 1, name: 'production'},
    {id: 2, name: 'staging'},
    {id: 3, name: 'development'},
];

export const mockAssets = [
    {id: 201, key: 'nginx.conf', version: 3, format: 'text', spaceId: 1},
    {id: 202, key: 'branding/logo.svg', version: 1, format: 'binary', spaceId: 1},
];

export const mockSecretRefs = [
    {id: 301, name: 'database-password', version: 4, spaceId: 1},
    {id: 302, name: 'github-token', version: 2, spaceId: 1},
];

export const mockConfigRefs = [
    {id: 401, name: 'database-host', version: 2, spaceId: 1},
    {id: 402, name: 'feature-flags', version: 7, spaceId: 1},
];

const apiConfig = {
    id: 101,
    version: 7,
    nodeId: 11,
    configId: {name: 'api', spaceId: 1},
    spec: {
        prepare: {containerImage: {image: 'ghcr.io/acme/api'}},
        runner: {container: {
            user: '1000:1000',
            command: ['/app/api', 'serve'],
            dataMountPath: '/var/lib/api',
            disableDataVolume: false,
            envVars: {
                APP_ENV: {value: 'production'},
                DATABASE_PASSWORD: {secretId: 301},
                DATABASE_HOST: {configId: 401},
            },
            assetMounts: [{assetId: 201, asset: 'nginx.conf', version: 3, path: '/etc/api/nginx.conf'}],
            upgradeStrategy: 2,
            readinessSignal: {timeoutSeconds: 90},
            devShmSizeKb: 131072,
            fileDescriptorLimit: 4096,
        }},
        networking: {
            mode: 1,
            portForwarding: [{protocol: 1, hostPort: 8443, containerPort: 8080}],
            ingress: [{kind: 1, hostname: 'api.example.test', tlsPassthroughConfig: {hostPort: 443, containerPort: 8443}}],
        },
    },
};

const workerConfig = {
    id: 102,
    version: 5,
    nodeId: 11,
    configId: {name: 'worker', spaceId: 2},
    spec: {
        prepare: {nixDockerBuild: {repo: 'github.com/acme/platform', flake: 'services/worker/flake.nix', target: '.#worker-image'}},
        runner: {container: {
            disableDataVolume: false,
            envVars: {
                APP_ENV: {value: 'staging'},
                API_ADDRESS: {addressDeploymentId: 101, addressSpaceId: 1},
            },
            upgradeStrategy: 1,
        }},
        networking: {mode: 1},
    },
};

const databaseConfig = {
    id: 103,
    version: 2,
    nodeId: 11,
    configId: {name: 'database', spaceId: 1},
    spec: {
        prepare: {containerImage: {image: 'postgres:17'}},
        runner: {container: {disableDataVolume: false, upgradeStrategy: 1}},
        networking: {mode: 1},
    },
};

export const mockDeployments = [apiConfig, workerConfig, databaseConfig].map(config => ({config, status: {}}));

const apiDeployment = {
    id: 101,
    name: 'api',
    spaceId: 1,
    variant: 'containerImage',
    deployedVersion: 'v2.8.1',
    desiredRunning: true,
    currentVersion: 7,
    runnerType: 'container',
};

const workerDeployment = {
    id: 102,
    name: 'worker',
    spaceId: 2,
    variant: 'nixDockerBuild',
    deployedVersion: 'c48d9b6f9c4a9a92b9f4dd25bfe5a3c671eca444',
    desiredRunning: false,
    currentVersion: 5,
    runnerType: 'container',
};

export const fixturePresets = {
    create: {
        label: 'Blank create',
        mode: 'create',
    },
    fork: {
        label: 'Fork API deployment',
        mode: 'create',
        deployment: apiDeployment,
        deploymentConfig: apiConfig,
        fork: true,
    },
    updateContainer: {
        label: 'Update running container',
        mode: 'update',
        deployment: apiDeployment,
        deploymentConfig: apiConfig,
    },
    updateNixStopped: {
        label: 'Update stopped Nix service',
        mode: 'update',
        deployment: workerDeployment,
        deploymentConfig: workerConfig,
    },
};

export const imageTags = [
    version('v2.9.0', 'Latest release', 18),
    version('v2.8.1', 'Current production release', 12),
    version('v2.8.0', 'Previous release', 3),
];

export const nixBranches = ['main', 'release/2026-07', 'feature/networking'];

export const nixCommits = {
    main: [
        version('be15f3a5e57d0569be3d8c10dca36a01da471c42', 'Improve deployment readiness', 19),
        version('c48d9b6f9c4a9a92b9f4dd25bfe5a3c671eca444', 'Current worker release', 14),
    ],
    'release/2026-07': [
        version('7ac5dd2bdc46a225a58ab757a4ef9213f2dc14d1', 'Prepare July release', 10),
    ],
    'feature/networking': [
        version('78c69c24847b6958678683e8b448d20b61d69bca', 'Route workload traffic', 17),
    ],
};
