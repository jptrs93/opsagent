export type Section = [id: string, title: string];
export type Document = { slug: string; title: string; sections: Section[] };
export type Group = { title: string; documents: Document[] };

export const groups: Group[] = [
  { title: 'Guides', documents: [
    { slug: 'guides/getting-started', title: 'Getting started', sections: [
      ['install', 'Install OpenDeploy'], ['sign-in', 'Sign in'], ['look-around', 'Look around'],
      ['first-deployment', 'Run your first deployment'], ['check', 'Check it works'], ['change', 'Change it'],
      ['uninstall', 'Uninstall'], ['next', 'Where next'],
    ] },
    { slug: 'guides/enrol-a-node', title: 'Enrol a node', sections: [
      ['prerequisites', 'Prerequisites'], ['install', 'Install the agent'], ['accept', 'Accept the request'],
      ['after', 'After enrolment'], ['remove', 'Remove a node'],
    ] },
    { slug: 'guides/expose-a-service', title: 'Expose a service', sections: [
      ['choose', 'Choose by the traffic'], ['https', 'HTTPS'], ['passthrough', 'TLS passthrough'],
      ['port-forward', 'Raw ports'], ['where', 'Where a route is published'],
    ] },
    { slug: 'guides/connect-two-deployments', title: 'Connect two deployments', sections: [
      ['reference', 'Reference the address'], ['dns', 'When a name is needed'], ['spaces', 'Across spaces'],
    ] },
    { slug: 'guides/manage-secrets-and-config', title: 'Manage secrets and config', sections: [
      ['create', 'Create a value'], ['reference', 'Use it in a deployment'], ['rotate', 'Change a value'], ['locked', 'If the store is locked'],
    ] },
    { slug: 'guides/deploy-from-nix-flake', title: 'Deploy from a Nix flake', sections: [
      ['flake', 'What the flake must provide'], ['deployment', 'The deployment'], ['private', 'Private repositories'], ['build', 'What happens on the node'],
    ] },
    { slug: 'guides/back-up-and-restore', title: 'Back up and restore', sections: [
      ['what', 'What is backed up'], ['enable', 'Enable backup'], ['recovery', 'Keep what a restore needs'], ['restore', 'Restore onto a new machine'],
    ] },
  ] },
  { title: 'Concepts & Design', documents: [
    { slug: 'design/introduction', title: 'Design introduction', sections: [['overview', 'Overview'], ['state', 'Cluster state'], ['reconciliation', 'Node reconciliation']] },
    { slug: 'design/networking', title: 'Networking', sections: [
      ['goals', 'Overview'], ['addressing', 'Addressing'], ['discovery', 'Service discovery and load balancing'],
      ['dns', 'DNS'], ['routing', 'Routing and transport'],
      ['rollover', 'Rollover'], ['policy', 'Network policy'], ['egress', 'Egress'], ['ingress', 'Ingress'],
    ] },
    { slug: 'design/logging', title: 'Logging', sections: [
      ['goals', 'Design overview'], ['structure', 'Parsed payload'], ['storage', 'Write-ahead log'],
      ['collector', 'Log collector'], ['search', 'Query engine'], ['system', 'System logs'], ['capture', 'Log consumer'],
    ] },
    { slug: 'design/deployments', title: 'Deployments', sections: [] },
    { slug: 'design/configuration-code', title: 'Configuration code', sections: [
      ['overview', 'Overview'], ['specification', 'Configuration code specification'], ['types', 'Type definition language'],
    ] },
    { slug: 'design/values-and-assets', title: 'Values and assets', sections: [] },
    { slug: 'design/access-control', title: 'Access control', sections: [] },
  ] },
  { title: 'Reference', documents: [
    { slug: 'reference/deployment', title: 'Deployment', sections: [
      ['type', 'Type definition'], ['code', 'Configuration code'], ['identity', 'Identity'], ['source', 'container.source'], ['process', 'container.process'],
      ['env-vars', 'container.env_vars'], ['mounts', 'container.mounts'], ['resources', 'container.resources'],
      ['upgrade', 'container.upgrade'], ['network', 'network'], ['ingress', 'network.ingress'],
      ['scheduling', 'scheduling'], ['functions', 'Functions'], ['related', 'Related design'],
    ] },
    { slug: 'reference/space', title: 'Space', sections: [] },
    { slug: 'reference/network-policy', title: 'Network policy', sections: [] },
    { slug: 'reference/domain-policy', title: 'Domain policy', sections: [] },
    { slug: 'reference/secret', title: 'Secret', sections: [] },
    { slug: 'reference/config-value', title: 'Config value', sections: [] },
    { slug: 'reference/asset', title: 'Asset', sections: [] },
    { slug: 'reference/node', title: 'Node', sections: [] },
    { slug: 'reference/node-access-policy', title: 'Node access policy', sections: [] },
    { slug: 'reference/user-and-access-rule', title: 'User and access rule', sections: [] },
    { slug: 'reference/system-settings', title: 'System settings', sections: [] },
  ] },
];
