import {expect} from '@playwright/test';
import {
  deploymentOutputOccurrenceCount,
  expectDeploymentOutput,
  expectDeploymentOutputOccurrences,
} from './ui.js';

// Assertions over `testexamples/netprobe` output. The probe reports DNS,
// connect and request as separate stages precisely so a policy drop is
// distinguishable from everything else that makes a request fail: a dropped
// packet is silent, so it can only ever surface as a connect timeout. A denied
// assertion that accepted any error would also pass when the name did not
// resolve, when the server never started, or when the whole virtual network
// was broken.

export const FLAKE = 'testexamples/netprobe/flake.nix';

export const okLine = (label) => `netprobe probe=${label} result=ok`;
// TCP drops surface on the handshake; UDP has none, so a dropped datagram
// surfaces as a response timeout instead.
export const deniedLine = (label, stage = 'connect') => `netprobe probe=${label} stage=${stage} result=error err=timeout`;
export const dnsOkLine = (label) => `netprobe probe=${label} stage=dns result=ok`;
export const streamTickLine = (label) => `netprobe stream=${label} tick=`;

export function internalUrl({deployment, spaceId, port = 8080, path = '/'}) {
  return `http://${deployment}.space-${spaceId}.internal:${port}${path}`;
}

// addressUrl targets a literal stable inbound address. Cross-node targets have
// to be literal: `.internal` names resolve only on the node holding the
// deployment, and an address() env ref cannot cross a space boundary.
export function addressUrl({address, port = 8080, path = '/'}) {
  return `http://[${address}]:${port}${path}`;
}

// The address line netprobe logs at startup; group B reads the server's own
// inbound address out of its output.
export const INBOUND_ADDRESS_PATTERN = /netprobe address name=\S+ inbound=([0-9a-f:]+)/;

export function targets(entries) {
  return Object.entries(entries).map(([label, url]) => `${label}=${url}`).join(',');
}

// expectProbeAllowed waits for `rounds` fresh successes, so it means "this
// probe succeeds now" for a probe that was already succeeding and for one that
// has just been unblocked, without either passing on stale output.
export async function expectProbeAllowed(page, {deployment, label, rounds = 2}) {
  const before = await deploymentOutputOccurrenceCount(page, deployment, okLine(label));
  await expectDeploymentOutputOccurrences(page, deployment, okLine(label), before + rounds);
}

// expectProbeDenied requires the failure to be the policy boundary: fresh
// connect timeouts, no successes in the same window, and DNS still resolving
// (the boundary drops payload, not name resolution — DNS to the netproxy is a
// default rule that crosses spaces).
export async function expectProbeDenied(page, {deployment, label, rounds = 2, expectDns = true, stage = 'connect'}) {
  const okBefore = await deploymentOutputOccurrenceCount(page, deployment, okLine(label));
  const deniedBefore = await deploymentOutputOccurrenceCount(page, deployment, deniedLine(label, stage));
  await expectDeploymentOutputOccurrences(page, deployment, deniedLine(label, stage), deniedBefore + rounds);
  const okAfter = await deploymentOutputOccurrenceCount(page, deployment, okLine(label));
  // Not equality: the log window these counts come from rolls, so an older
  // success can age out between the two reads. A boundary that was actually
  // open would add successes faster than the window drops them.
  expect(okAfter, `probe ${label} must not succeed while denied`).toBeLessThanOrEqual(okBefore);
  if (expectDns) await expectDeploymentOutput(page, deployment, [dnsOkLine(label)]);
}

export async function expectProbeBytes(page, {deployment, label, bytes}) {
  await expectDeploymentOutput(page, deployment, [`netprobe probe=${label} result=ok status=200 bytes=${bytes}`]);
}

export async function probeDeniedCount(page, {deployment, label, stage = 'connect'}) {
  return deploymentOutputOccurrenceCount(page, deployment, deniedLine(label, stage));
}

// expectProbeNotDenied asserts that no denial appeared since the count
// taken before a disruption. Take the count first, then let the disruption run
// to completion, then call this — a window that does not span the event proves
// nothing.
export async function expectProbeNotDenied(page, {deployment, label, since, stage = 'connect'}) {
  const now = await probeDeniedCount(page, {deployment, label, stage});
  // Not equality: the log window these counts come from rolls, so an older
  // denial can age out between the two reads.
  expect(now, `probe ${label} must not be denied across the transition`).toBeLessThanOrEqual(since);
}

export async function streamTickCount(page, {deployment, label}) {
  return deploymentOutputOccurrenceCount(page, deployment, streamTickLine(label));
}

export async function expectStreamAdvanced(page, {deployment, label, from, ticks = 5}) {
  await expectDeploymentOutputOccurrences(page, deployment, streamTickLine(label), from + ticks);
}
