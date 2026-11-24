import assert from "node:assert/strict";
import test from "node:test";
import {
    MODE_EXACT,
    MODE_EXCEPT,
    allDomainsExcluding,
    allowEffect,
    canonicalDomain,
    createPayload,
    decide,
    denyEffect,
    describeDomains,
    describeSpaces,
    draftFromPolicy,
    draftProblems,
    draftRule,
    exactDomains,
    isDeny,
    newDraft,
    openToEverySpace,
    parseDomains,
    patternCovers,
    readDomains,
    ruleSentence,
    togglePayload,
    updatePayload,
    userSpaces,
} from "./domainPolicies.js";

const SPACES = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "staging"}, {id: 3, name: "prod"}];
const NAMES = new Map(SPACES.map((s) => [s.id, s.name]));
const STAGING = 2, PROD = 3;

const exactSpaces = (...ids) => ({exactSpaces: {values: ids}});
const allSpacesExcluding = (...ids) => ({allSpacesExcluding: {values: ids}});
const policy = (id, name, deny, spaces, domains, extra = {}) => ({
    id, seq: 100 + id, name, disabled: false,
    rule: {effect: deny ? denyEffect() : allowEffect(), selector: {spaces, domains}},
    ...extra,
});
const defaultPolicy = policy(1, "default_ingress_domains", false, allSpacesExcluding(), allDomainsExcluding());
const prodOnly = policy(2, "prod-only", true, allSpacesExcluding(PROD), exactDomains(["example.com", "*.example.com"]));

test("the effect is a oneof of two empty messages", () => {
    assert.deepEqual(allowEffect(), {value: {allow: {}}});
    assert.deepEqual(denyEffect(), {value: {deny: {}}});
    assert.equal(isDeny({effect: denyEffect()}), true);
    assert.equal(isDeny({effect: allowEffect()}), false);
});

test("readDomains reads both lists, as decoded with the other one undefined", () => {
    assert.deepEqual(readDomains({exactDomains: undefined, allDomainsExcluding: {values: []}}), {mode: "all", values: []});
    assert.deepEqual(readDomains(allDomainsExcluding(["a.test"])), {mode: "allExcept", values: ["a.test"]});
    assert.deepEqual(readDomains({exactDomains: {values: ["a.test"]}, allDomainsExcluding: undefined}), {mode: "list", values: ["a.test"]});
    assert.deepEqual(readDomains(exactDomains([])), {mode: "none", values: []});
    assert.deepEqual(readDomains(undefined), {mode: "none", values: []});
});

test("selector summaries read naturally and resolve space names", () => {
    assert.equal(describeSpaces(allSpacesExcluding(), NAMES), "all spaces");
    assert.equal(describeSpaces(allSpacesExcluding(PROD, STAGING), NAMES), "all spaces except prod, staging");
    assert.equal(describeSpaces(exactSpaces(STAGING, 9), NAMES), "staging, space #9");
    assert.equal(describeSpaces(undefined, NAMES), "no spaces");
    assert.equal(describeDomains(allDomainsExcluding()), "all domains");
    assert.equal(describeDomains(allDomainsExcluding(["*.internal.test"])), "all domains except *.internal.test");
    assert.equal(describeDomains(exactDomains(["example.com", "*.example.com"])), "example.com, *.example.com");
    assert.equal(describeDomains(undefined), "no domains");
    assert.equal(ruleSentence(prodOnly.rule, NAMES), "all spaces except prod may not publish example.com, *.example.com");
    assert.equal(ruleSentence(defaultPolicy.rule, NAMES), "all spaces may publish all domains");
});

test("canonicalDomain accepts hostnames and wildcards of one, in canonical form", () => {
    assert.equal(canonicalDomain(" API.Example.com. "), "api.example.com");
    assert.equal(canonicalDomain("*.Example.COM"), "*.example.com");
    assert.equal(canonicalDomain("localhost"), "localhost");
    assert.equal(canonicalDomain("a-1.b2.test"), "a-1.b2.test");
    for (const bad of ["", "*", "*.", "a.*.test", "a.test/prod", "a..test", "-a.test", "a-.test", "a_b.test", "http://a.test", "a.test..", "**.a.test", ".a.test"]) {
        assert.equal(canonicalDomain(bad), null, bad);
    }
    assert.equal(canonicalDomain("a".repeat(63) + ".test"), "a".repeat(63) + ".test");
    assert.equal(canonicalDomain("a".repeat(64) + ".test"), null);
    const longest = Array(4).fill("a".repeat(62)).join(".") + ".bb";
    assert.equal(longest.length, 254);
    assert.equal(canonicalDomain(longest), null);
    assert.equal(canonicalDomain(longest.slice(1)), longest.slice(1));
    assert.equal(canonicalDomain("*." + longest.slice(1)), "*." + longest.slice(1), "the limit is on the hostname, not the wildcard");
});

test("parseDomains splits on lines and commas, dedupes, and reports what is not a domain", () => {
    assert.deepEqual(parseDomains("Example.com\n*.example.com, example.com.\n\n  api.example.com  "),
        {domains: ["example.com", "*.example.com", "api.example.com"], invalid: []});
    assert.deepEqual(parseDomains("ok.test, Not_ok, *"), {domains: ["ok.test"], invalid: ["Not_ok", "*"]});
    assert.deepEqual(parseDomains(""), {domains: [], invalid: []});
});

test("a wildcard covers every depth below its suffix and never the suffix", () => {
    assert.equal(patternCovers("*.example.com", "api.example.com"), true);
    assert.equal(patternCovers("*.example.com", "a.b.example.com"), true);
    assert.equal(patternCovers("*.example.com", "example.com"), false);
    assert.equal(patternCovers("*.example.com", "notexample.com"), false);
    assert.equal(patternCovers("example.com", "example.com"), true);
    assert.equal(patternCovers("example.com", "api.example.com"), false);
});

test("decide checks enabled denies first, then enabled allows, and refuses the rest", () => {
    const disabledDeny = policy(3, "off", true, allSpacesExcluding(), allDomainsExcluding(), {disabled: true});
    const set = [defaultPolicy, prodOnly, disabledDeny];
    const one = [policy(4, "one", false, exactSpaces(STAGING), exactDomains(["a.test"]))];
    const rest = [policy(5, "rest", false, allSpacesExcluding(), allDomainsExcluding(["*.internal.test"]))];
    const cases = [
        ["no policy refuses", [], PROD, "example.com", {allowed: false, policy: ""}],
        ["the default allows anything", [defaultPolicy], STAGING, "anything.test", {allowed: true, policy: ""}],
        ["a deny beats the allow", set, STAGING, "example.com", {allowed: false, policy: "prod-only"}],
        ["the excluded space keeps the name", set, PROD, "example.com", {allowed: true, policy: ""}],
        ["a wildcard covers any depth", set, STAGING, "a.b.example.com", {allowed: false, policy: "prod-only"}],
        ["a wildcard is not a suffix match", set, STAGING, "notexample.com", {allowed: true, policy: ""}],
        ["a disabled deny is ignored", set, STAGING, "other.test", {allowed: true, policy: ""}],
        ["a disabled allow is ignored", [{...defaultPolicy, disabled: true}], STAGING, "other.test", {allowed: false, policy: ""}],
        ["an exact allow covers only its names", one, STAGING, "b.test", {allowed: false, policy: ""}],
        ["an exact allow covers only its spaces", one, PROD, "a.test", {allowed: false, policy: ""}],
        ["all-but domains leaves the listed out", rest, PROD, "db.internal.test", {allowed: false, policy: ""}],
        ["a selector with neither list covers nothing", [policy(6, "bare", false, exactSpaces(STAGING), {})], STAGING, "a.test", {allowed: false, policy: ""}],
    ];
    for (const [name, policies, space, hostname, want] of cases) assert.deepEqual(decide(policies, space, hostname), want, name);
});

test("the open notice needs an enabled allow over everything and more than one user space", () => {
    assert.deepEqual(userSpaces(SPACES).map((s) => s.id), [1, 2, 3]);
    assert.equal(openToEverySpace([defaultPolicy, prodOnly], SPACES), true);
    assert.equal(openToEverySpace([defaultPolicy], SPACES.slice(0, 2)), false, "one user space");
    assert.equal(openToEverySpace([{...defaultPolicy, disabled: true}], SPACES), false);
    assert.equal(openToEverySpace([policy(7, "deny-all", true, allSpacesExcluding(), allDomainsExcluding())], SPACES), false);
    assert.equal(openToEverySpace([policy(8, "most", false, allSpacesExcluding(PROD), allDomainsExcluding())], SPACES), false);
    assert.equal(openToEverySpace([policy(9, "some", false, allSpacesExcluding(), exactDomains(["a.test"]))], SPACES), false);
    assert.equal(openToEverySpace([], SPACES), false);
});

test("a new draft is an enabled deny that still needs a name and a domain", () => {
    const draft = newDraft();
    assert.deepEqual(draftProblems(draft), ["Name the policy.", "List at least one domain."]);
    assert.deepEqual(draftProblems({...draft, name: "p", domainsText: "a.test"}), []);
    assert.deepEqual(draftProblems({...draft, name: "p", spacesMode: MODE_EXACT, domainsMode: MODE_EXCEPT}), ["Pick at least one space."]);
    assert.deepEqual(draftProblems({...draft, name: "p", domainsText: "a.test, b_c"}), ['Not a hostname or a "*." wildcard of one: b_c']);
});

test("a draft builds the rule with exactly one list per position", () => {
    assert.deepEqual(draftRule({...newDraft(), spaceIds: [PROD], domainsText: "Example.com, *.example.com"}), {
        effect: {value: {deny: {}}},
        selector: {spaces: {allSpacesExcluding: {values: [PROD]}}, domains: {exactDomains: {values: ["example.com", "*.example.com"]}}},
    });
    assert.deepEqual(draftRule({...newDraft(), deny: false, spacesMode: MODE_EXACT, spaceIds: [STAGING], domainsMode: MODE_EXCEPT}), {
        effect: {value: {allow: {}}},
        selector: {spaces: {exactSpaces: {values: [STAGING]}}, domains: {allDomainsExcluding: {values: []}}},
    });
});

test("a policy round trips through its draft into the update payload", () => {
    const draft = draftFromPolicy(prodOnly);
    assert.deepEqual(draft, {
        name: "prod-only", deny: true, enabled: true,
        spacesMode: MODE_EXCEPT, spaceIds: [PROD],
        domainsMode: MODE_EXACT, domainsText: "example.com\n*.example.com",
    });
    assert.deepEqual(updatePayload(prodOnly, draft), {id: 2, expectedSeq: 102, name: "prod-only", disabled: false, rule: prodOnly.rule});
    assert.deepEqual(draftFromPolicy(defaultPolicy), {
        name: "default_ingress_domains", deny: false, enabled: true,
        spacesMode: MODE_EXCEPT, spaceIds: [],
        domainsMode: MODE_EXCEPT, domainsText: "",
    });
    assert.deepEqual(createPayload({...draft, name: "  copy ", enabled: false}), {name: "copy", disabled: true, rule: prodOnly.rule});
});

test("a toggle sends the whole policy with only disabled flipped", () => {
    assert.deepEqual(togglePayload(prodOnly), {id: 2, expectedSeq: 102, name: "prod-only", disabled: true, rule: prodOnly.rule});
    assert.equal(togglePayload({...prodOnly, disabled: true}).disabled, false);
});
