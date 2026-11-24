// Domain policies fixture: the shipped Spaces page (src/pages/spaces.js) on
// its Domain policies tab, over stores seeded here and an in-memory version
// of the three /v1/space-domain-policies endpoints. Run from frontend/ with
// `pnpm run dev:domain-policies`.
//
// The mock refuses what the server refuses: a write that would leave one of
// PUBLISHED without permission (409, the server's sentence), a duplicate
// name, a stale expectedSeq. Disabling or deleting default_ingress_domains
// is the quick way to see a refusal. `#data=empty` opens with no policies.
import van from "vanjs-core";
import {capi} from "/src/capi/index.js";
import {spaceDomainPoliciesS, spacesS} from "/src/state/deployments.js";
import {allDomainsExcluding, allowEffect, decide, denyEffect, exactDomains} from "/src/lib/domainPolicies.js";
import {spacesPage} from "/src/pages/spaces.js";

const {div} = van.tags;

const SPACES = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "staging"}, {id: 3, name: "prod"}];

// The ingress hostnames deployments already publish.
const PUBLISHED = [
    {hostname: "shop.example.com", deployment: "storefront", spaceId: 3},
    {hostname: "api.staging.example.net", deployment: "api", spaceId: 2},
];

const policy = (id, name, effect, spaces, domains, disabled = false) =>
    ({id, seq: 40 + id, name, disabled, rule: {effect, selector: {spaces, domains}}});

const DATASETS = {
    typical: () => [
        policy(1, "default_ingress_domains", allowEffect(), {allSpacesExcluding: {values: []}}, allDomainsExcluding()),
        policy(2, "example_com_is_prod", denyEffect(), {allSpacesExcluding: {values: [3]}}, exactDomains(["example.com", "*.example.com"])),
        policy(3, "staging_previews", allowEffect(), {exactSpaces: {values: [2]}}, exactDomains(["*.preview.example.net"]), true),
    ],
    empty: () => [],
};

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
// The shape capi/err.js throws: the message is the server's displayErr.
const refuse = (status, displayErr) => Object.assign(new Error(displayErr), {status, apiErr: {displayErr}});

const requireRoutesAllowed = (policies) => {
    const names = new Map(spacesS.val.map((s) => [s.id, s.name]));
    const stranded = PUBLISHED.filter((route) => !decide(policies, route.spaceId, route.hostname).allowed)
        .map((route) => `${route.hostname} of deployment "${route.deployment}" in space "${names.get(route.spaceId)}"`);
    if (stranded.length) throw refuse(409, `This change would leave published hostnames without permission: ${stranded.join("; ")}`);
};

let seq = 100;
const commit = (others, next) => {
    if (others.some((p) => p.name === next.name)) throw refuse(409, "A domain policy with this name already exists");
    requireRoutesAllowed([...others, next]);
    spaceDomainPoliciesS.val = [...others, next].sort((a, b) => a.id - b.id);
};
const existing = (id) => {
    const current = spaceDomainPoliciesS.val.find((p) => p.id === id);
    if (!current) throw refuse(404, "Domain policy not found");
    return current;
};
const without = (id) => spaceDomainPoliciesS.val.filter((p) => p.id !== id);

Object.assign(capi, {
    async postV1SpaceDomainPoliciesCreate({name, disabled, rule}) {
        await delay(250);
        const id = spaceDomainPoliciesS.val.reduce((max, p) => Math.max(max, p.id), 0) + 1;
        commit(spaceDomainPoliciesS.val, {id, seq: ++seq, name, disabled, rule});
    },
    async postV1SpaceDomainPoliciesUpdate({id, expectedSeq, name, disabled, rule}) {
        await delay(250);
        if (expectedSeq && existing(id).seq > expectedSeq) throw refuse(409, "Domain policy was modified concurrently");
        commit(without(id), {id, seq: ++seq, name, disabled, rule});
    },
    async postV1SpaceDomainPoliciesDelete({id}) {
        await delay(250);
        existing(id);
        requireRoutesAllowed(without(id));
        spaceDomainPoliciesS.val = without(id);
    },
});

const dataset = new URLSearchParams(window.location.hash.slice(1)).get("data");
spacesS.val = SPACES;
spaceDomainPoliciesS.val = (DATASETS[dataset] || DATASETS.typical)();

van.add(document.body, div({class: "h-dvh w-dvw overflow-hidden"}, spacesPage()));
document.querySelector('[data-testid="spaces-tab-domain-policies"]').click();
