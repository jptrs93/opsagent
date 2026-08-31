/*
 * The data-model page content: objects (shown as trimmed type definitions,
 * derived from api-contract/*.proto) and the relationships between them,
 * laid out user-updated -> derived.
 *
 * Conventions:
 *  - a card = an object with its own referencable identity or artifact life
 *  - an inline block = an owned sub-object (no independent identity;
 *    addressed as <root id> + path); expanded by default, click to collapse
 *  - the blue "versioned" tag marks a type whose changes are explicitly
 *    version-tracked; the counter itself is implied, never shown as a field
 *  - the amber "id" chip marks a root's identity fields
 *  - rose field names are server-set: stamped by the server on write, never
 *    client input (timestamps, author)
 *  - enums are chips, collapsed by default: hover for the values, click to
 *    expand them
 */
(function () {
  'use strict';

  var g = GraphKit.createGraph(document.getElementById('canvas'));

  /* schema helpers */
  function f(name, type, opts) {
    opts = opts || {};
    return { name: name, type: type, mod: opts.mod, ref: opts.ref, note: opts.note, key: opts.key, srv: opts.srv, versioned: opts.versioned };
  }
  function obj(name, fields, opts) {
    opts = opts || {};
    return { kind: 'object', name: name, fields: fields, versioned: opts.versioned };
  }
  function en(name, values) { return { kind: 'enum', name: name, values: values }; }
  function v(name, note) { return { name: name, note: note }; }

  /* shared types */
  var INGRESS_KIND = en('IngressKind', ['TLS_PASSTHROUGH', 'HTTPS']);
  var HTTP_BACKEND = en('HttpBackendProtocol', [v('H2C', 'unset = HTTP/1.1')]);
  var INGRESS_BACKEND = obj('IngressBackend', [
    f('address', 'string', { note: 'stable inbound address I' }),
    f('port', 'int32'),
  ]);

  /* ---------- layout regions ---------- */

  g.addNote({
    x: 40, y: 8, w: 940,
    html: 'Objects flow left to right: configuration written by users on the left, state the ' +
      'system derives from it on the right. Cards show trimmed type definitions — click a ' +
      'highlighted type to expand the sub-object or enum it names.',
  });

  g.addRegion({ x: 40,   y: 100, w: 480, h: 760, title: 'User updated' });
  g.addRegion({ x: 640,  y: 100, w: 540, h: 760, title: 'Derived — cluster-wide' });
  g.addRegion({ x: 1300, y: 100, w: 510, h: 760, title: 'Derived — node-local' });

  /* ---------- objects ---------- */

  g.addNode({
    id: 'node',
    title: 'ClusterNode',
    badge: 'user updated',
    tone: 'user',
    x: 80, y: 160, w: 400,
    desc: 'A machine enrolled into the cluster. Written once at enrollment, then edited by operators (name, allowed spaces).',
    schema: [
      f('id', 'int32', { key: true }),
      f('enrollmentId', 'int32', { ref: 'enrollment request' }),
      f('name', 'string'),
      f('identifier', 'string', { note: 'immutable machine identifier' }),
      f('roles', 'int32', { mod: 'repeated' }),
      f('wgPublicKey', 'string', { note: 'base64 Curve25519' }),
      f('addresses', 'string', { mod: 'repeated' }),
      f('timestamp', 'timestamp', { srv: true }),
      f('allowedSpaces', 'int32', { mod: 'repeated', ref: 'Space.id' }),
    ],
  });

  g.addNode({
    id: 'deployment',
    title: 'Deployment',
    badge: 'user updated',
    tone: 'user',
    versioned: true,
    x: 80, y: 560, w: 400,
    desc: 'The user’s declared intent for one deployment — everything on the right is rendered from this.',
    schema: [
      f('id', 'int32', { key: true }),
      f('name', 'string', { versioned: true }),
      f('spaceAssignment', obj('SpaceAssignment', [
        f('spaceId', 'int32', { ref: 'Space.id' }),
      ], { versioned: true })),
      f('nodeId', 'int32', { ref: 'ClusterNode.id' }),
      f('author', 'int32', { ref: 'user id', srv: true }),
      f('timestamp', 'timestamp', { srv: true }),
      f('deleted', 'bool'),
      f('spec', obj('DeploymentSpec', [
        f('networking', obj('NetworkingConfig', [
          f('mode', en('NetworkingMode', [
            v('VIRTUAL', 'per-container netns'),
            v('HOST', 'opt-out: host netns'),
          ])),
          f('portForwarding', obj('PortForward', [
            f('protocol', en('PortForwardProtocol', ['TCP', 'UDP'])),
            f('hostPort', 'int32'),
            f('containerPort', 'int32'),
            f('ipFilter', obj('IpFilter', [
              f('allow', 'string', { mod: 'repeated', note: 'IPs / CIDRs' }),
              f('deny', 'string', { mod: 'repeated', note: 'rejected on write today' }),
            ])),
          ]), { mod: 'repeated' }),
          f('ingress', obj('Ingress', [
            f('kind', INGRESS_KIND),
            f('hostname', 'string'),
            f('tlsPassthroughConfig', obj('TlsPassthroughConfig', [
              f('hostPort', 'int32', { note: '0 = 443' }),
              f('containerPort', 'int32'),
            ])),
            f('httpsConfig', obj('HttpsConfig', [
              f('containerPort', 'int32'),
              f('pathPrefix', 'string'),
              f('stripPrefix', 'bool'),
              f('backendProtocol', HTTP_BACKEND),
              f('certSource', obj('CertSource', [
                f('acme', obj('AcmeCertSource', [
                  f('challenge', en('AcmeChallenge', ['HTTP_01'])),
                ])),
                f('secret', obj('SecretCertSource', [
                  f('secretVersionId', 'int32', { ref: 'secret version' }),
                ])),
              ])),
            ])),
          ]), { mod: 'repeated' }),
        ])),
        f('container1Spec', obj('ContainerSpec', [
          f('source', obj('ContainerBundleSource', [
            f('nixDockerBuild', obj('NixDockerBuild', [
              f('repo', 'string'),
              f('flake', 'string'),
              f('target', 'string'),
            ])),
            f('remoteImage', obj('RemoteDockerImage', [
              f('image', 'string'),
            ])),
          ])),
          f('version', 'string'),
          f('running', 'bool'),
          f('upgradeStrategy', en('ContainerUpgradeStrategy', [
            v('RECREATE', 'stop old, start new'),
            v('ROLLOVER', 'candidate warms beside the old run'),
          ])),
          f('runtime', obj('ContainerRuntime', [
            f('user', 'string'),
            f('envVars', obj('EnvVarValue', [
              f('value', 'string', { mod: 'optional' }),
              f('secretVersionId', 'int32', { mod: 'optional', ref: 'secret version' }),
              f('configVersionId', 'int32', { mod: 'optional', ref: 'config version' }),
              f('assetVersionId', 'int32', { ref: 'asset version' }),
              f('addressSpaceId', 'int32', { mod: 'optional', note: 'typed address ref' }),
              f('addressDeploymentId', 'int32', { mod: 'optional', ref: 'Deployment.id' }),
            ]), { mod: 'map<string, ·>', note: 'one form per value' }),
            f('defaultVolume', obj('DefaultVolumeMount', [
              f('containerPath', 'string'),
              f('disabled', 'bool'),
            ])),
            f('assetMounts', obj('AssetMount', [
              f('assetVersionId', 'int32', { ref: 'asset version' }),
              f('containerPath', 'string'),
              f('permission', en('FilePermission', ['READ_WRITE', 'READ_ONLY', 'READ_EXECUTE'])),
            ]), { mod: 'repeated' }),
            f('issuedTlsMount', obj('IssuedTLSMount', [
              f('containerPath', 'string'),
              f('extraNames', 'string', { mod: 'repeated' }),
              f('caOnly', 'bool'),
            ])),
          ])),
        ]), { note: 'one workload field set' }),
      ], { versioned: true })),
    ],
    note: 'Each write creates a new immutable version; workers act on the latest.',
  });

  g.addNode({
    id: 'netmap',
    title: 'ClusterNetMap',
    badge: 'derived',
    tone: 'derived',
    x: 680, y: 340, w: 440,
    desc: 'Complete placement + underlay snapshot rendered by the primary, targeted to one node. Workers persist an accepted map, apply it to the kernel (WireGuard peers, routes, policy), and report both stamps back.',
    schema: [
      f('targetNodeId', 'int32', { ref: 'ClusterNode.id' }),
      f('derivedFromSeq', 'int64', { note: 'global write seq at render' }),
      f('ulaPrefix', 'bytes', { note: '6-byte ULA /48' }),
      f('nodes', obj('ClusterNetMapNode', [
        f('nodeId', 'int32', { ref: 'ClusterNode.id' }),
        f('underlayAddress', 'string'),
        f('wgPublicKey', 'string', { note: 'always set; WireGuard is the only transport' }),
        f('wgListenPort', 'int32'),
      ]), { mod: 'repeated' }),
      f('routes', obj('ClusterNetMapRoute', [
        f('logicalPrefix', 'string', { note: '/100 instance or /120 placement' }),
        f('hostingNodeId', 'int32', { ref: 'ClusterNode.id' }),
      ]), { mod: 'repeated' }),
      f('policyRules', obj('NetPolicyRule', [
        f('source', obj('NetPolicyPeer', [
          f('spaceId', 'int32'),
          f('deploymentId', 'int32', { note: '0 = whole space' }),
        ])),
        f('destination', obj('NetPolicyPeer', [
          f('spaceId', 'int32'),
          f('deploymentId', 'int32', { note: '0 = whole space' }),
        ])),
        f('ports', obj('NetPortMatch', [
          f('protocol', en('NetProtocol', ['TCP', 'UDP'])),
          f('port', 'int32'),
          f('portEnd', 'int32', { note: '0 = single port' }),
        ]), { mod: 'repeated', note: 'empty = all' }),
      ]), { mod: 'repeated' }),
    ],
    note: 'Routes carry only /100 (instance) and /120 (placement) prefixes — container restarts never appear in the map.',
  });

  g.addNode({
    id: 'netstate',
    title: 'NetState',
    badge: 'derived · node-local',
    tone: 'local',
    x: 1340, y: 300, w: 430,
    desc: 'Per-node snapshot the agent writes atomically into the dataplane deployment’s data volume. netproxy file-watches it and serves DNS and ingress from it — no sockets, no DB, no deltas.',
    schema: [
      f('seq', 'int64', { note: 'monotonic; stale snapshots ignored' }),
      f('ulaPrefix', 'bytes'),
      f('nodeIdentifier', 'string', { ref: 'ClusterNode.identifier' }),
      f('dnsServices', obj('DnsService', [
        f('name', 'string', { note: 'normalized deployment name' }),
        f('environment', 'string', { note: 'normalized space name' }),
        f('endpoints', obj('Endpoint', [
          f('ordinal', 'int32'),
          f('address', 'string', { note: 'stable inbound address I' }),
          f('state', en('EndpointState', [
            v('READY', 'in DNS, receives traffic'),
            v('DRAINING', 'removed ahead of SIGTERM'),
            v('DOWN', 'instance not running'),
          ])),
          f('nodeId', 'int32', { ref: 'ClusterNode.id' }),
        ]), { mod: 'repeated' }),
      ]), { mod: 'repeated' }),
      f('upstreamResolvers', 'string', { mod: 'repeated' }),
      f('ingress', obj('NetIngress', [
        f('kind', INGRESS_KIND),
        f('hostname', 'string'),
        f('tlsPassthrough', obj('TlsPassthroughNetIngress', [
          f('hostPort', 'int32'),
          f('backends', INGRESS_BACKEND, { mod: 'repeated' }),
        ])),
        f('https', obj('HttpsNetIngress', [
          f('pathPrefix', 'string'),
          f('stripPrefix', 'bool'),
          f('backendProtocol', HTTP_BACKEND),
          f('certId', 'string'),
          f('backends', INGRESS_BACKEND, { mod: 'repeated' }),
        ])),
      ]), { mod: 'repeated' }),
      f('acmeChallenges', obj('AcmeHttpChallenge', [
        f('token', 'string'),
        f('keyAuthorization', 'string'),
      ]), { mod: 'repeated' }),
    ],
    note: 'DNS answers list READY endpoints only; a known service with none gets an authoritative empty answer.',
  });

  /* ---------- relationships ---------- */

  g.addEdge({
    from: 'node', to: 'netmap',
    kind: 'derive', fromAt: 0.55, toAt: 0.2,
    label: 'underlay + WG key → nodes[]',
  });
  g.addEdge({
    from: 'deployment', to: 'netmap',
    kind: 'derive', fromAt: 0.3, toAt: 0.55,
    label: ['placements → routes[]', 'policies → policyRules[]'],
  });
  g.addEdge({
    from: 'deployment', to: 'netstate',
    kind: 'derive', fromAt: 0.62, toAt: 0.6, labelAt: 0.13,
    label: ['name, endpoints, ingress spec', '→ dnsServices[] + ingress[]'],
  });
  g.addEdge({
    from: 'deployment', to: 'node',
    kind: 'ref', fromSide: 'top', toSide: 'bottom',
    label: 'nodeId → id (placement)',
  });
  g.addEdge({
    from: 'netmap', to: 'netstate',
    kind: 'ref', fromAt: 0.08, toAt: 0.08,
    label: 'same global write seq',
  });

  g.render();

  /* ---------- chrome wiring ---------- */

  GraphKit.initSplitter(
    document.getElementById('sidebar'),
    document.getElementById('splitter'));

  var pct = document.getElementById('zoom-pct');
  g.onZoom(function (s) { pct.textContent = Math.round(s * 100) + '%'; });
  document.getElementById('zoom-in').addEventListener('click', function () { g.zoomStep(1.25); });
  document.getElementById('zoom-out').addEventListener('click', function () { g.zoomStep(0.8); });
  document.getElementById('zoom-fit').addEventListener('click', function () { g.fit(); });
})();
