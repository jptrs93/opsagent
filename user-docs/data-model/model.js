/*
 * The data-model page content: objects (shown as trimmed type definitions,
 * derived from api-contract/*.proto) and the relationships between them,
 * laid out user-updated -> derived.
 *
 * Conventions:
 *  - a card = an object with its own referencable identity or artifact life
 *  - an inline block = an owned sub-object (no independent identity;
 *    addressed as <root id> + path); expanded by default, click to collapse
 *  - the small "v" marker (hover it) tags parts of the state tree that are
 *    explicitly version-tracked; versioned sub-parts render as their own
 *    tinted block; the counter itself is implied, never shown as a field
 *  - the amber "id" chip marks a root's identity fields
 *  - grey fields with a "read only" badge are server-set: stamped by the
 *    server, never client input (timestamps, author); id fields keep their
 *    amber chip but carry the "read only" badge too; badges right-align
 *  - enums are chips, collapsed by default: hover for the values, click to
 *    expand them
 *  - every persisted root is an event log: the *Event envelope (id, event id,
 *    global seq, author, event type, created/event time) wraps the immutable
 *    value written by that event; the browser state stream carries envelopes
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
  var EVENT_TYPE = en('EventType', ['CREATE', 'UPDATE', 'DELETE']);
  var INGRESS_KIND = en('IngressKind', ['TLS_PASSTHROUGH', 'HTTPS']);
  var HTTP_BACKEND = en('HttpBackendProtocol', [v('H2C', 'unset = HTTP/1.1')]);
  var NET_PROTOCOL = en('NetProtocol', ['TCP', 'UDP']);
  var INGRESS_BACKEND = obj('IngressBackend', [
    f('address', 'string', { note: 'stable inbound address I' }),
    f('port', 'int32'),
  ]);
  var NET_PORT_MATCH = obj('NetPortMatch', [
    f('protocol', NET_PROTOCOL),
    f('port', 'int32'),
    f('portEnd', 'int32', { note: '0 = single port' }),
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
    title: 'NodeEvent',
    badge: 'operator and node updated',
    tone: 'user',
    versioned: true,
    x: 80, y: 160, w: 400,
    desc: 'A versioned machine record. Operators own membership and placement permissions; the node reports the addresses and identity used by cluster plans. Connection observations travel separately as NodeStatus: an append-only history with a nanosecond updatedAt clock. The live view selects the latest observation; empty observations clear status and retain their clock. All history is kept.',
    schema: [
      f('nodeId', 'int32', { key: true, srv: true }),
      f('eventId', 'int64', { srv: true }),
      f('seq', 'int64', { srv: true, note: 'one sequence per transaction' }),
      f('author', 'int32', { ref: 'user id', srv: true }),
      f('eventType', EVENT_TYPE, { srv: true }),
      f('createdTime', 'timestamp', { srv: true }),
      f('eventTime', 'timestamp', { srv: true }),
      f('value', obj('Node', [
        f('status', en('NodeLifecycleStatus', [
          v('ENROLLMENT_REQUESTED'),
          v('ENROLLMENT_CANCELLED'),
          v('ENROLLMENT_REQUEST_EXPIRED'),
          v('MEMBER_NORMAL'),
          v('MEMBER_UNHEALTHY'),
          v('MEMBER_DRAINING'),
          v('MEMBER_MISSING'),
          v('MEMBER_EVICTED'),
        ])),
        f('enrollmentRequestedAt', 'int64', { srv: true, note: '0 = no pending request' }),
        f('operator', obj('NodeOperator', [
          f('name', 'string', { note: 'unique' }),
          f('roles', 'int32', { mod: 'repeated' }),
          f('allowedSpaces', 'int32', { mod: 'repeated', ref: 'Space.id' }),
          f('enrolledTime', 'int64', { srv: true }),
        ])),
        f('reported', obj('NodeReported', [
          f('identifier', 'string', { note: 'immutable machine identifier' }),
          f('underlayAddress', 'string'),
          f('wgPublicKey', 'string', { note: 'base64 Curve25519' }),
          f('hostAddresses', 'string', { mod: 'repeated', note: 'stable host addresses; ingress listen inventory' }),
          f('hostAddressesUnknown', 'bool', { note: 'failed inventory keeps the last known list' }),
        ])),
      ])),
    ],
  });

  g.addNode({
    id: 'deployment',
    title: 'DeploymentEvent',
    badge: 'user updated',
    tone: 'user',
    versioned: true,
    x: 80, y: 780, w: 400,
    desc: 'The user’s declared intent for one deployment — everything on the right is rendered from this. The envelope columns are authoritative: eventType is the deletion truth (no deleted flag) and the value holds only the caller-owned definition.',
    schema: [
      f('deploymentId', 'int32', { key: true, srv: true }),
      f('eventId', 'int64', { srv: true }),
      f('seq', 'int64', { srv: true, note: 'one sequence per transaction' }),
      f('value', obj('Deployment', [
      f('name', 'string', { versioned: true }),
      f('spaceId', 'int32', { ref: 'Space.id', versioned: true }),
      f('nodeId', 'int32', { ref: 'NodeEvent.nodeId' }),
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
          ]), { mod: 'repeated', note: 'virtual mode only' }),
          f('ingress', obj('Ingress', [
            f('kind', INGRESS_KIND),
            f('hostname', 'string'),
            f('listen', obj('IngressListen', [
              f('node', obj('NodeSelector', [
                f('any', 'bool', { note: 'every node that can reach the backend' }),
                f('nodeId', 'int32', { ref: 'NodeEvent.nodeId', note: 'at most one of any / nodeId' }),
              ]), { note: 'unset = the scheduled node(s)' }),
              f('address', obj('AddressSelector', [
                f('family', en('AddressFamily', ['ANY', 'IPV4', 'IPV6'])),
                f('prefixes', 'string', { mod: 'repeated', note: 'IP / CIDR; empty = every address' }),
              ])),
            ]), { mod: 'repeated', note: 'union of selectors; empty = all host addresses of the scheduled node(s)' }),
            f('tlsPassthroughConfig', obj('TlsPassthroughConfig', [
              f('hostPort', 'int32', { note: '0 = 443' }),
              f('containerPort', 'int32'),
            ])),
            f('httpsConfig', obj('HttpsConfig', [
              f('containerPort', 'int32'),
              f('pathPrefix', 'string'),
              f('stripPrefix', 'bool'),
              f('backendProtocol', HTTP_BACKEND),
              f('maxRequestBodyBytes', 'int64', { note: '0 = default' }),
              f('flushIntervalMs', 'int32', { note: '0 = default' }),
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
          f('readinessSignal', obj('ContainerReadinessSignal', [
            f('timeoutSeconds', 'int32', { note: '0 = default' }),
          ]), { note: 'used by ROLLOVER' }),
          f('runtime', obj('ContainerRuntime', [
            f('user', 'string'),
            f('envVars', obj('EnvVarValue', [
              f('value', 'string', { mod: 'optional' }),
              f('secretVersionId', 'int32', { mod: 'optional', ref: 'secret version' }),
              f('configVersionId', 'int32', { mod: 'optional', ref: 'config version' }),
              f('assetVersionId', 'int32', { ref: 'asset version' }),
              f('addressSpaceId', 'int32', { mod: 'optional', note: 'typed address ref' }),
              f('addressDeploymentId', 'int32', { mod: 'optional', ref: 'DeploymentEvent.deploymentId' }),
            ]), { mod: 'map<string, ·>', note: 'one form per value' }),
            f('defaultVolume', obj('DefaultVolumeMount', [
              f('containerPath', 'string'),
              f('disabled', 'bool'),
            ])),
            f('crossDeploymentMounts', obj('CrossDeploymentMount', [
              f('deploymentId', 'int32', { ref: 'DeploymentEvent.deploymentId' }),
              f('containerPath', 'string'),
              f('permission', en('FilePermission', ['READ_WRITE', 'READ_ONLY', 'READ_EXECUTE'])),
            ]), { mod: 'repeated', note: 'another deployment’s default volume' }),
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
      ])),
      f('author', 'int32', { ref: 'user id', srv: true }),
      f('eventType', EVENT_TYPE, { srv: true }),
      f('createdTime', 'timestamp', { srv: true }),
      f('eventTime', 'timestamp', { srv: true }),
    ],
    note: 'Each write creates an immutable event. The browser holds the latest desired version plus versions pinned by retained instances; secondaries receive their pinned assignment.',
  });

  g.addNode({
    id: 'netpolicy',
    title: 'NetworkPolicyEvent',
    badge: 'user updated',
    tone: 'user',
    versioned: true,
    x: 80, y: 2720, w: 400,
    desc: 'One global override connectivity rule: the source peer may initiate connections toward the destination peer. Policies are first-class entities with their own history — not part of any deployment spec. Peers are single-id anchors: a deployment peer’s space resolves at render time, so space moves follow automatically.',
    schema: [
      f('networkPolicyId', 'int32', { key: true, srv: true }),
      f('eventId', 'int64', { srv: true }),
      f('seq', 'int64', { srv: true, note: 'one sequence per transaction' }),
      f('author', 'int32', { ref: 'user id', srv: true }),
      f('eventType', EVENT_TYPE, { srv: true }),
      f('createdTime', 'timestamp', { srv: true }),
      f('eventTime', 'timestamp', { srv: true }),
      f('value', obj('NetworkPolicy', [
        f('action', en('NetworkPolicyAction', [
          v('ALLOW'),
          v('DENY', 'schema-reserved; rejected on write'),
        ])),
        f('source', obj('NetworkPolicyPeerRef', [
          f('kind', en('NetworkPolicyPeerKind', ['SPACE', 'DEPLOYMENT'])),
          f('id', 'int32', { ref: 'Space.id or DeploymentEvent.deploymentId' }),
        ])),
        f('destination', obj('NetworkPolicyPeerRef', [
          f('kind', en('NetworkPolicyPeerKind', ['SPACE', 'DEPLOYMENT'])),
          f('id', 'int32', { ref: 'Space.id or DeploymentEvent.deploymentId' }),
        ])),
        f('ports', NET_PORT_MATCH, { mod: 'repeated', note: 'empty = all ports and protocols' }),
      ])),
    ],
  });

  g.addNode({
    id: 'schedinst',
    title: 'ScheduledInstanceEvent',
    badge: 'derived',
    tone: 'derived',
    versioned: true,
    x: 690, y: 150, w: 420,
    desc: 'One placement of a deployment instance ordinal on a node, created by the scheduler and pinned to one deployment version — the immutable deployment value it runs. Every target change appends a version; there is no author because only the scheduler writes it. Cross-node routing is a pure function of these assignments — no status input. Workers receive ScheduledInstanceState instead: the placement, its pinned DeploymentEvent, and the latest observed ScheduledInstanceStatus.',
    schema: [
      f('scheduledInstanceId', 'int32', { key: true, srv: true }),
      f('eventId', 'int64', { srv: true }),
      f('seq', 'int64', { srv: true, note: 'one sequence per transaction' }),
      f('eventType', EVENT_TYPE, { srv: true, note: 'CREATE for version 1, else UPDATE' }),
      f('createdTime', 'timestamp', { srv: true }),
      f('eventTime', 'timestamp', { srv: true }),
      f('value', obj('ScheduledInstance', [
        f('deploymentId', 'int32', { ref: 'DeploymentEvent.deploymentId' }),
        f('deploymentVersion', 'int32', { note: 'pins one immutable deployment version' }),
        f('deploymentSpecVersion', 'int32', { note: 'denormalised from the pinned version; keys prepared artifacts' }),
        f('nodeId', 'int32', { ref: 'NodeEvent.nodeId' }),
        f('instanceOrdinal', 'int32'),
        f('spaceId', 'int32', { ref: 'Space.id', note: 'denormalised from the pinned version' }),
        f('state', en('ScheduledInstanceTarget', [
          v('RUN_SERVING', 'owns the stable inbound address'),
          v('RUN_STANDBY', 'warming replacement, not yet serving'),
          v('RUN_DRAINING', 'superseded but still running'),
          v('TERMINATE', 'node should terminate'),
          v('FINALIZED', 'termination acknowledged'),
        ])),
      ])),
    ],
    note: 'Exactly one placement per (deployment, ordinal) is RUN_SERVING at a time.',
  });

  g.addNode({
    id: 'netmap',
    title: 'ClusterNetMap',
    badge: 'derived',
    tone: 'derived',
    x: 680, y: 1000, w: 440,
    desc: 'Complete placement + underlay snapshot rendered by the primary, targeted to one node. Secondaries persist an accepted map, apply it to the kernel (WireGuard peers, routes, policy, ingress DNAT), and report both stamps back.',
    schema: [
      f('targetNodeId', 'int32', { ref: 'NodeEvent.nodeId' }),
      f('derivedFromSeq', 'int64', { note: 'global write seq at render' }),
      f('ulaPrefix', 'bytes', { note: '6-byte ULA /48' }),
      f('nodes', obj('ClusterNetMapNode', [
        f('nodeId', 'int32', { ref: 'NodeEvent.nodeId' }),
        f('underlayAddress', 'string'),
        f('wgPublicKey', 'string', { note: 'always set; WireGuard is the only transport' }),
        f('wgListenPort', 'int32'),
        f('ingressPublish', obj('IngressPublish', [
          f('address', 'string', { note: 'empty = every local address' }),
          f('port', 'int32'),
        ]), { mod: 'repeated', note: 'this node’s DNAT set to netproxy, from listen selectors × host addresses' }),
      ]), { mod: 'repeated' }),
      f('routes', obj('ClusterNetMapRoute', [
        f('logicalPrefix', 'string', { note: '/100 instance or /120 placement' }),
        f('hostingNodeId', 'int32', { ref: 'NodeEvent.nodeId' }),
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
        f('ports', NET_PORT_MATCH, { mod: 'repeated', note: 'empty = all' }),
      ]), { mod: 'repeated', note: 'allow rules; peer spaces resolved at render' }),
      f('dnsServices', obj('ClusterNetMapService', [
        f('name', 'string', { note: 'normalized deployment name' }),
        f('spaceId', 'int32'),
        f('deploymentId', 'int32', { ref: 'DeploymentEvent.deploymentId' }),
        f('ordinals', obj('ClusterNetMapServiceOrdinal', [
          f('ordinal', 'int32'),
        ]), { mod: 'repeated', note: 'ordinals with a serving placement' }),
      ]), { mod: 'repeated', note: 'cluster-wide DNS catalog' }),
    ],
    note: 'Routes carry only /100 (instance) and /120 (placement) prefixes — container restarts never appear in the map.',
  });

  g.addNode({
    id: 'netstate',
    title: 'NetState',
    badge: 'derived · node-local',
    tone: 'local',
    x: 1340, y: 1900, w: 430,
    desc: 'Per-node snapshot the agent writes atomically into the dataplane deployment’s data volume. netproxy file-watches it and serves DNS and ingress from it — no sockets, no DB, no deltas.',
    schema: [
      f('seq', 'int64', { note: 'monotonic; stale snapshots ignored' }),
      f('ulaPrefix', 'bytes'),
      f('nodeIdentifier', 'string', { ref: 'NodeEvent.value.reported.identifier' }),
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
          f('maxRequestBodyBytes', 'int64'),
          f('flushIntervalMs', 'int32'),
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
    kind: 'derive', fromAt: 0.6, toAt: 0.12, labelAt: 0.75,
    label: ['underlay, WG key, host addresses', '→ nodes[]'],
  });
  g.addEdge({
    from: 'deployment', to: 'schedinst',
    kind: 'derive', fromAt: 0.08, toAt: 0.5, labelAt: 0.6,
    label: ['scheduler creates placements', 'pinned to one deployment version'],
  });
  g.addEdge({
    from: 'schedinst', to: 'netmap',
    kind: 'derive',
    label: ['placements → routes[]', 'serving ordinals → dnsServices[]'],
  });
  g.addEdge({
    from: 'deployment', to: 'netmap',
    kind: 'derive', fromAt: 0.22, toAt: 0.3,
    label: ['listen → ingressPublish[]', 'names → dnsServices[]'],
  });
  g.addEdge({
    from: 'netpolicy', to: 'netmap',
    kind: 'derive', fromAt: 0.5, toAt: 0.7, labelAt: 0.55,
    label: 'allow rules → policyRules[]',
  });
  g.addEdge({
    from: 'deployment', to: 'netstate',
    kind: 'derive', fromAt: 0.75, toAt: 0.4, labelAt: 0.72,
    label: ['name, endpoints, ingress spec', '→ dnsServices[] + ingress[]'],
  });
  g.addEdge({
    from: 'deployment', to: 'node',
    kind: 'ref', fromSide: 'top', toSide: 'bottom',
    label: 'nodeId → id (placement)',
  });
  g.addEdge({
    from: 'netpolicy', to: 'deployment',
    kind: 'ref', fromSide: 'top', toSide: 'bottom',
    label: 'peer id → deploymentId',
  });
  g.addEdge({
    from: 'netmap', to: 'netstate',
    kind: 'ref', fromSide: 'right', fromAt: 0.92, toSide: 'left', toAt: 0.05, labelAt: 0.5,
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
