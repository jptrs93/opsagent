import {
  createNixDockerDeployment,
  expectDeploymentOutput,
  expectDeploymentOutputDistinctMatches,
  expectDeploymentOutputOccurrences,
  expectDeploymentRunning,
  expectIssuedTLSHclDiagnostics,
  NETWORKING_VIRTUAL,
  setDeploymentIssuedTLSMount,
} from '../helpers/ui.js';

const SERVER = 'issued-tls-server';
const CLIENT = 'issued-tls-client';
const MOUNT_PATH = '/opendeploy-tls';
// Issued certificates always use the space-id based internal name, so the
// default space (id 1) yields <name>.space-1.internal.
const SERVER_DNS = `${SERVER}.space-1.internal`;
const EXTRA_NAME = 'issued-tls-extra.test';
const ROTATED_NAME = 'issued-tls-rotated.test';
const SERVE_PORT = '8443';
const FINGERPRINT_PATTERN = /issuedtls fingerprint=([0-9a-f]{64})/g;

export const issuedTLSCases = [
  {
    id: 'issued-tls-server-created',
    title: 'create issued TLS server deployment',
    description: 'Creates a virtual-network deployment, adds an issued_tls mount with an extra name through the HCL editor, and waits for it to run.',
    requires: ['nix-docker-virtual-network'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: SERVER,
        machine: 'worker-1',
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          OPENDEPLOY_E2E_ISSUED_TLS_DIR: MOUNT_PATH,
          OPENDEPLOY_E2E_ISSUED_TLS_SERVE_PORT: SERVE_PORT,
        },
        expectedEnv: {},
      });
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, extraNames: [EXTRA_NAME]},
      });
      await expectDeploymentRunning(ctx.page, {name: SERVER, machine: 'worker-1'});
    },
  },
  {
    id: 'issued-tls-material-verified',
    title: 'verify issued TLS material in container',
    description: 'Verifies the mounted public.crt/private.key/ca.crt files: matching key pair, deployment DNS identity, extra-name SAN, one virtual-network IP SAN, and a chain to the workload CA.',
    requires: ['issued-tls-server-created'],
    async run(ctx) {
      await expectDeploymentOutput(ctx.page, SERVER, [
        'issuedtls file public.crt ok=true',
        'issuedtls file private.key ok=true',
        'issuedtls file ca.crt ok=true',
        'issuedtls keypair=ok',
        `issuedtls subject=${SERVER_DNS}`,
        `issuedtls san dns=${SERVER_DNS}`,
        `issuedtls san dns=${EXTRA_NAME}`,
        // The signer always appends localhost/loopback SANs; virtual mode
        // adds the deployment's inbound IPv6, so 3 IP SANs in total.
        'issuedtls san dns=localhost',
        'issuedtls san ipcount=3',
        `issuedtls chain verified=true name=${SERVER_DNS}`,
      ]);
    },
  },
  {
    id: 'issued-tls-handshake-verified',
    title: 'verify cross-node TLS handshake with a ca_only client',
    description: 'Creates a worker-2 client with a ca_only issued_tls mount (workload CA only, no leaf cert/key) that dials the worker-1 server over the virtual network and verifies the server certificate against the mounted workload CA.',
    requires: ['issued-tls-server-created', 'worker-2-enrolled'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: CLIENT,
        machine: 'worker-2',
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          OPENDEPLOY_E2E_ISSUED_TLS_DIR: MOUNT_PATH,
          OPENDEPLOY_E2E_ISSUED_TLS_CONNECT_HOST: {type: 'address', name: SERVER},
          OPENDEPLOY_E2E_ISSUED_TLS_CONNECT_PORT: SERVE_PORT,
          OPENDEPLOY_E2E_ISSUED_TLS_SERVER_NAME: SERVER_DNS,
        },
        expectedEnv: {},
      });
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: CLIENT,
        machine: 'worker-2',
        mount: {containerPath: MOUNT_PATH, caOnly: true},
      });
      await expectDeploymentOutput(ctx.page, CLIENT, [
        'issuedtls file ca.crt ok=true',
        // ca_only mounts must not contain leaf material.
        'issuedtls file public.crt present=false',
        'issuedtls file private.key present=false',
        'issuedtls mode=ca-only',
        'issuedtls client verified=true status=200 body=issued-tls-server ok',
      ]);
    },
  },
  {
    id: 'issued-tls-editor-diagnostics',
    title: 'verify issued_tls HCL editor diagnostics',
    description: 'Checks the code editor rejects duplicate issued_tls mounts, mount options, and non-list extra_names without saving.',
    requires: ['issued-tls-server-created'],
    async run(ctx) {
      await expectIssuedTLSHclDiagnostics(ctx.page, {
        name: SERVER,
        checks: [
          {
            mounts: [`mount(issued_tls(), "${MOUNT_PATH}")`, 'mount(issued_tls(), "/other-tls")'],
            diagnostic: 'Only one issued_tls mount is allowed.',
          },
          {
            mounts: [`mount(issued_tls(), "${MOUNT_PATH}", { read_only = true })`],
            diagnostic: 'issued_tls mounts do not accept mount options.',
          },
          {
            mounts: [`mount(issued_tls({ extra_names = "${EXTRA_NAME}" }), "${MOUNT_PATH}")`],
            diagnostic: 'extra_names must be a list of quoted strings.',
          },
          {
            mounts: [`mount(issued_tls({ extra_names = ["${EXTRA_NAME}"], ca_only = true }), "${MOUNT_PATH}")`],
            diagnostic: 'extra_names are not allowed with ca_only.',
          },
        ],
      });
    },
  },
  {
    id: 'issued-tls-reissued-on-update',
    title: 'verify certificate reissue on config change',
    description: 'Adds an extra name, which bumps the config version, and verifies a fresh certificate carrying the new SAN with a new fingerprint is issued.',
    requires: ['issued-tls-material-verified'],
    async run(ctx) {
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, extraNames: [EXTRA_NAME, ROTATED_NAME]},
      });
      await expectDeploymentOutput(ctx.page, SERVER, [
        `issuedtls san dns=${ROTATED_NAME}`,
      ]);
      await expectDeploymentOutputDistinctMatches(ctx.page, SERVER, FINGERPRINT_PATTERN, 2);
    },
  },
  {
    id: 'issued-tls-invalid-config-rejected',
    title: 'verify invalid issued_tls configs are rejected',
    description: 'Submits a relative containerPath and an invalid extra name and verifies the API rejects both updates.',
    requires: ['issued-tls-reissued-on-update'],
    async run(ctx) {
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: 'relative/tls'},
        expectError: 'containerPath must be absolute',
      });
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, extraNames: ['bad name']},
        expectError: 'is not a valid host name or IP address',
      });
    },
  },
  {
    id: 'issued-tls-mount-removed-and-restored',
    title: 'remove and restore the issued_tls mount',
    description: 'Removes the mount and verifies the certificate files disappear from the container, then restores it and verifies a fresh certificate is issued.',
    requires: ['issued-tls-invalid-config-rejected'],
    async run(ctx) {
      await setDeploymentIssuedTLSMount(ctx.page, {name: SERVER, mount: null});
      // The first container run predates the mount, so removal produces the
      // second missing-directory line.
      await expectDeploymentOutputOccurrences(ctx.page, SERVER, 'issuedtls dir error=', 2);
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, extraNames: [EXTRA_NAME, ROTATED_NAME]},
      });
      await expectDeploymentOutputOccurrences(ctx.page, SERVER, 'issuedtls keypair=ok', 3);
      await expectDeploymentOutputDistinctMatches(ctx.page, SERVER, FINGERPRINT_PATTERN, 3);
      await expectDeploymentRunning(ctx.page, {name: SERVER, machine: 'worker-1'});
    },
  },
  {
    id: 'issued-tls-ca-only-downgrade',
    title: 'downgrade the server mount to ca_only and restore',
    description: 'Switches the full issued_tls mount to ca_only, verifies the leaf cert and key are removed from the mount while ca.crt remains, then restores the full mount and verifies a fresh certificate is issued.',
    requires: ['issued-tls-mount-removed-and-restored'],
    async run(ctx) {
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, caOnly: true},
      });
      await expectDeploymentOutput(ctx.page, SERVER, [
        'issuedtls file public.crt present=false',
        'issuedtls file private.key present=false',
        'issuedtls mode=ca-only',
      ]);
      await setDeploymentIssuedTLSMount(ctx.page, {
        name: SERVER,
        mount: {containerPath: MOUNT_PATH, extraNames: [EXTRA_NAME, ROTATED_NAME]},
      });
      await expectDeploymentOutputOccurrences(ctx.page, SERVER, 'issuedtls keypair=ok', 4);
      await expectDeploymentOutputDistinctMatches(ctx.page, SERVER, FINGERPRINT_PATTERN, 4);
      await expectDeploymentRunning(ctx.page, {name: SERVER, machine: 'worker-1'});
    },
  },
];
