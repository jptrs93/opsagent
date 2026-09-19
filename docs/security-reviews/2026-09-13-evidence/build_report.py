#!/usr/bin/env python3
"""Build the 13 September 2026 re-review HTML from finding data and HEAD excerpts."""
import collections
import html
import json
import pathlib
import re
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[3]
EVIDENCE = pathlib.Path(__file__).resolve().parent
COMMIT = "2ea3af1e8f141b0fff85012d1ed766af6c35b16f"
BASELINE = "8e28a9f1b79e850ea773811448089616cc48fb24"
RECHECK = "60d73bcb152cf52a4650b394d9ff505dc71f63cc"
RECHECK_DATE = "16 September 2026"
RELEASE = "v0.0.609"
BASE = f"https://github.com/jptrs93/opsagent/blob/{COMMIT}/"
OUT = EVIDENCE.parent / "2026-09-13-opendeploy-security-re-review.html"
e = html.escape

from findings_data import FINDINGS, CLOSED, ACCEPTED  # noqa: E402

SEVERITY_ORDER = {"Critical": 0, "High": 1, "Medium": 2, "Low": 3}
ORIGIN_LABEL = {
    "audit": "11 Sep audit",
    "review": "12 Sep assessment",
    "new": "New in this re-review",
}


def source_link(path, line, commit=None):
    base = f"https://github.com/jptrs93/opsagent/blob/{commit}/" if commit else BASE
    return f'<a href="{e(base+path)}#L{line}" target="_blank" rel="noopener noreferrer">{e(path)}:{line}</a>'


def excerpt(path, first, last):
    lines = subprocess.check_output(["git", "show", f"{COMMIT}:{path}"], cwd=ROOT, text=True).splitlines()
    code = "\n".join(f"{i:4}  {lines[i-1]}" for i in range(first, min(last, len(lines)) + 1))
    return f'<div class="excerpt">{source_link(path, first)}<pre><code>{e(code)}</code></pre></div>'


def refs(items):
    return " · ".join(f'<a href="{e(url)}" target="_blank" rel="noopener noreferrer">{e(title)}</a>' for title, url in items)


def badge(sev):
    return f'<span class="badge {sev.lower()}">{e(sev)}</span>'


ordered = sorted(FINDINGS, key=lambda f: (SEVERITY_ORDER[f["severity"]], f["id"]))
counts = collections.Counter(f["severity"] for f in FINDINGS)

cards = []
for f in ordered:
    sections = "".join(
        f'<div><h4>{label}</h4><p>{e(f[key])}</p></div>'
        for key, label in [
            ("trigger", "Prerequisites"),
            ("evidence", "Why it happens"),
            ("validation", "Validation and limits"),
            ("fix", "Recommended fix"),
            ("close", "Closure criteria"),
        ]
    )
    prior = f'<span class="prior">Previously {e(f["prior"])}</span>' if f.get("prior") else ""
    cards.append(f'''<article class="finding" id="{f['id']}" data-severity="{f['severity'].lower()}" data-origin="{f['origin']}">
    <div class="finding-meta">{badge(f['severity'])}<span>{f['id']} · {e(f['area'])}</span><span class="origin">{e(ORIGIN_LABEL[f['origin']])}</span>{prior}<span class="verification">{e(f['status'])}</span></div>
    <h3>{e(f['title'])}</h3><p class="impact">{e(f['impact'])}</p>
    <div class="finding-body">{sections}</div>
    <details><summary>Source evidence <span>{e(f['cwe'])}</span></summary>{''.join(excerpt(*s) for s in f['sources'])}</details>
    {'<p class="references">'+refs(f['refs'])+'</p>' if f.get('refs') else ''}</article>''')

index_rows = "".join(
    f'<tr><td><a href="#{f["id"]}">{f["id"]}</a></td><td>{badge(f["severity"])}</td><td><a href="#{f["id"]}">{e(f["title"])}</a></td>'
    f'<td>{e(ORIGIN_LABEL[f["origin"]])}{("<small>was " + e(f["prior"]) + "</small>") if f.get("prior") else ""}</td><td>{e(f["status"])}</td></tr>'
    for f in ordered
)
closed_rows = "".join(
    f'<tr><td>{e(c["id"])}</td><td>{e(c["title"])}</td><td>{badge(c["prior"])}</td><td>{e(c["how"])}</td><td>{" ".join(source_link(p, l, c.get("commit")) for p, l in c["sources"])}</td></tr>'
    for c in CLOSED
)
stats = "".join(
    f'<div class="stat {name.lower()}"><strong>{counts[name]}</strong><span>{name}</span></div>'
    for name in ["Critical", "High", "Medium", "Low"]
)
stats += f'<div class="stat closed"><strong>{len(CLOSED)}</strong><span>Closed since 11 Sep</span></div>'
stats += f'<div class="stat accepted"><strong>{len(ACCEPTED)}</strong><span>Accepted by design</span></div>'
accepted_rows = "".join(
    f'<tr><td>{e(a["id"])}</td><td>{e(a["title"])}</td><td>{badge(a["prior"])}</td><td><code>{e(a["position"])}</code></td><td>{e(a["why"])}</td><td>{" ".join(source_link(p, l) for p, l in a["sources"])}</td></tr>'
    for a in ACCEPTED
)

host = json.loads((EVIDENCE / "govulncheck-host-summary.json").read_text())
linux = json.loads((EVIDENCE / "govulncheck-linux-summary.json").read_text())
symbol_ids = {f["id"] for f in host["symbol_findings"]} | {f["id"] for f in linux["symbol_findings"]}
triage = {
    "GO-2026-5970": "golang.org/x/text iterator loop on invalid input. Reachable symbol; input path is hostnames and labels handled by the IDNA and normalisation code. Bump to v0.39.0.",
    "GO-2026-5764": "AWS SDK EventStream decoder panic. Reachable through the S3 backup client; the attacker would need to control the S3 endpoint's responses. Bump service/s3 to v1.97.3.",
    "GO-2026-6061": "gRPC xDS RBAC and HTTP/2 server transport. OpenDeploy uses gRPC only as a client to the local containerd socket; no gRPC server or xDS. Bump to v1.82.1 on the next dependency pass.",
}
go_rows = []
seen = set()
for f in host["module_findings"] + linux["module_findings"]:
    if f["id"] in seen:
        continue
    seen.add(f["id"])
    level = "symbol" if f["id"] in symbol_ids else "module only"
    go_rows.append(
        f'<tr><td><a href="https://pkg.go.dev/vuln/{f["id"]}">{f["id"]}</a><small>{e(f["summary"])}</small></td>'
        f'<td><code>{e(f["module"])}</code><small>{e(f.get("version") or "")} → {e(f.get("fixed_version") or "no fix listed")}</small></td>'
        f'<td>{e(level)}</td><td>{e(triage.get(f["id"], "Module-level match only; no vulnerable symbol reached from OpenDeploy code."))}</td></tr>'
    )
audit = json.loads((EVIDENCE / "pnpm-audit.json").read_text())
npm_rows = []
for a in audit["advisories"].values():
    installed = ", ".join(sorted({x["version"] for x in a.get("findings", [])}))
    npm_rows.append(
        f'<tr><td>{e(a["module_name"])} <small>{e(installed)}</small></td><td><a href="{e(a["url"])}">{e(a["github_advisory_id"])}</a><small>{e(a["title"])}</small></td>'
        f'<td>{e(a["severity"])}</td><td><code>{e(a["patched_versions"])}</code></td></tr>'
    )
npm_counts = collections.Counter(a["severity"] for a in audit["advisories"].values())

repro = (EVIDENCE / "reproduction-results.txt").read_text()
go_tests = (EVIDENCE / "go-tests.txt").read_text()
fe_tests = (EVIDENCE / "frontend-tests.txt").read_text()
go_passed = len(re.findall(r"^ok\s", go_tests, re.M))
go_failed = len(re.findall(r"^FAIL\s", go_tests, re.M))
go_untested = len(re.findall(r"\[no test files\]", go_tests))
fe_passed = int(re.search(r"^ℹ pass (\d+)", fe_tests, re.M).group(1))
fe_failed = int(re.search(r"^ℹ fail (\d+)", fe_tests, re.M).group(1))
fe_skipped = int(re.search(r"^ℹ skipped (\d+)", fe_tests, re.M).group(1))
repro_rows = []
for name, meaning in [
    ("TestAuditAgentCanBindDeniedSecret", "OD-05 agent binds a denied secret (fixture uses host networking; now denied by the use_host_network gate before the secret path runs, see corrected test below). Accepted by design, see AP-1"),
    ("TestAuditSpaceOperatorCanMountProtectedParent", "OD-02 space_admin mounts /var/lib (denied by the use_host_mounts and use_host_network gates)"),
    ("TestAuditPasskeyUpdatesAppendDuplicateIDs", "OD-12 duplicate passkey rows"),
    ("TestAuditMountParentAndSymlinkValidation", "OD-02 parent path and symlink accepted"),
    ("TestAuditUnownedCertificateNameAccepted", "OD-04 unowned name passes the syntax-only mount validator (closed after the review by 23ae086: the space check runs in the create/update layer and the issuer, see od-04-closure-results.txt)"),
    ("TestAuditWorkloadCertificateVerifiesForUnownedName", "OD-04 certificate verifies for another space's name (closed after the review by 23ae086: the issuer now rejects the name and this test fails, see od-04-closure-results.txt)"),
    ("TestAuditRegistryRealmCallsLoopback", "OD-11 registry realm reaches loopback"),
    ("TestAuditRegistryPaginationForwardsBearerToAnotherOrigin", "OD-11 bearer forwarded to another origin"),
    ("TestReviewAgentCanBindDeniedSecretWithVirtualNetworking", "OD-05 corrected: agent binds a denied secret with virtual networking. Behaviour confirmed and accepted by design, see AP-1"),
    ("TestReviewSpaceAdminHostMountDeniedWithVirtualNetworking", "OD-02 corrected: space_admin host mount denied even for an unprotected path"),
]:
    passed = f"--- PASS: {name}" in repro
    if name == "TestReviewSpaceAdminHostMountDeniedWithVirtualNetworking":
        verdict, cls = ("Denied as expected", "fixed") if passed else ("Unexpected", "open")
    else:
        verdict = "Still reproduces" if passed else "No longer reproduces"
        cls = "open" if passed else "fixed"
    repro_rows.append(f'<tr><td><code>{e(name)}</code></td><td>{e(meaning)}</td><td><span class="badge {cls}">{verdict}</span></td></tr>')

CSS = (EVIDENCE / "_style.css").read_text()
JS = (EVIDENCE / "_script.js").read_text()

document = f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><meta name="referrer" content="no-referrer"><title>OpenDeploy — Security Re-review · 13 September 2026</title><style>{CSS}</style></head>
<body><a class="skip" href="#findings">Skip to findings</a><div class="topbar"><div class="shell"><div class="brand">OPENDEPLOY / SECURITY REVIEW</div><span>Re-review of the 11 September audit · 13 September 2026 · status updated {RECHECK_DATE}</span></div></div>
<header class="hero shell"><div class="eyebrow">Security re-review / September 2026</div><h1>The host boundary is largely closed. The tenant boundary is not.</h1><p class="lede">Since the 11 September audit, Nix builds moved into a locked-down container, host mounts and host networking became cluster-admin-only, and the runtime and HTTP/2 dependencies were updated. What remains sits mostly between tenants and at the edges: hostnames are not bound to spaces, workloads have no resource or syscall limits, host ports and request paths are not arbitrated, and the primary's public listeners have no deadlines. Updated {RECHECK_DATE}: node eviction shipped in {RELEASE} and closes OD-07; every other finding was re-checked at commit {RECHECK[:7]} and remains open.</p><div class="meta"><span>Reviewed 13 September 2026 · Australia/Sydney</span><span>Commit <code>{COMMIT[:12]}</code></span><span>Baseline <code>{BASELINE[:12]}</code></span><span>Re-checked {RECHECK_DATE} at <code>{RECHECK[:12]}</code> ({RELEASE})</span><span>Source review, rerun reproductions, dependency scans</span></div><div class="scorecard">{stats}<div class="stat-note"><strong>{len(FINDINGS)} open findings</strong>{sum(1 for f in FINDINGS if f['origin']=='audit')} carried from the 11 September audit, {sum(1 for f in FINDINGS if f['origin']=='review')} from the 12 September assessment that followed it, {sum(1 for f in FINDINGS if f['origin']=='new')} new. Every item was re-verified against commit {COMMIT[:7]} on 13 September and re-checked at {RECHECK[:7]} on {RECHECK_DATE}; anything closed in between has moved to the closed table.</div></div></header>
<nav class="nav" aria-label="Report sections"><div class="shell"><a href="#assessment">Assessment</a><a href="#closed">Closed</a><a href="#findings">Open findings</a><a href="#remediation">Remediation</a><a href="#dependencies">Dependencies</a><a href="#coverage">Validation</a><button id="print" type="button">Print / save PDF</button></div></nav>
<main class="shell"><section id="assessment"><div class="intro-grid"><div class="callout"><strong>Bind identity to the space, then bound the workload.</strong><p>The two Critical paths from September 11 are closed or gated to cluster administrators. The remaining High items share one shape: something a space-scoped operator can name, claim or bind is honoured cluster-wide without checking that the space owns it. Fix those as a set, then give workloads the resource and syscall limits that build containers already have.</p><div class="flow" aria-label="Observed escalation path"><span>Space-scoped write</span><b aria-hidden="true">→</b><span>Cluster-wide name, port or key</span><b aria-hidden="true">→</b><span>Another tenant's traffic or credentials</span></div></div><div class="scope"><p><strong>Scope.</strong> Every finding of the 11 September audit (OD-01 to OD-13) and every additional item raised in the 12 September assessment, re-verified against the Go backend, frontend, API contract and installer at commit {COMMIT[:12]}. Diffs against the audited baseline were used to establish what changed.</p><p><strong>Method.</strong> Source review with file and line evidence; the audit's eight overlay reproductions rerun unchanged against HEAD; the full backend and frontend test suites; govulncheck for host and Linux targets; pnpm audit. Linux mounts, escapes and load tests were not executed.</p><p><strong>Identifiers.</strong> OD-nn are the audit's original identifiers and keep their numbers. RR-nn identify items first raised in the 12 September assessment or in this re-review. Where a finding narrowed, the previous severity is shown beside the new one.</p></div></div>
<p class="small" style="margin-top:22px">Critical = plausible host/control-plane compromise from lesser deployment authority. High = major confidentiality, integrity or shared-service impact under stated prerequisites. Medium = bounded, conditional or availability/hardening exposure. Low = hygiene or defence in depth. No CVSS scores were assigned.</p>
<h2 style="margin-top:30px">Open finding index</h2><div class="table-wrap"><table><thead><tr><th>ID</th><th>Severity</th><th>Finding</th><th>Origin</th><th>Evidence level</th></tr></thead><tbody>{index_rows}</tbody></table></div></section>
<section id="closed"><div class="eyebrow">Closed since 11 September</div><h2>What the intervening commits fixed.</h2><p class="small">Each closure was confirmed by reading the current code and, where the audit had a reproduction, by that reproduction no longer passing. Fixed code does not retire credentials or state exposed before the fix. OD-07 closed after the review, in {RELEASE}; its evidence links point at commit {RECHECK[:12]}.</p><div class="table-wrap"><table><thead><tr><th>ID</th><th>Finding</th><th>Was</th><th>How it closed</th><th>Evidence</th></tr></thead><tbody>{closed_rows}</tbody></table></div>
<h2 style="margin-top:30px">Accepted by design</h2><p class="small">Behaviour that is present and reproducible but recorded as documented authority in <code>docs/engineering/auth.md</code> under <em>Accepted positions</em>. Per <code>security-severity.md</code> such items are Informational; a future review that disagrees should argue against the position rather than reopen the finding.</p><div class="table-wrap"><table><thead><tr><th>ID</th><th>Finding</th><th>Was</th><th>Position</th><th>Why it is accepted</th><th>Evidence</th></tr></thead><tbody>{accepted_rows}</tbody></table></div></section>
<section id="findings"><div class="eyebrow">Evidence-led review</div><h2>Open findings</h2><p class="small">Source links point to commit {COMMIT[:12]}; excerpts are embedded so the report reads offline. Reproduced behaviour is distinguished from a full exploit. Recommendations have not been applied. Each finding below was re-checked at commit {RECHECK[:12]} on {RECHECK_DATE}: the commits in between touch none of these code paths, so the pinned excerpts still describe the current tree.</p><div class="filters"><label>Search findings<br><input id="search" type="search" placeholder="Search a component, risk or finding ID…"></label><label>Severity<br><select id="severity"><option value="">All severities</option><option value="critical">Critical</option><option value="high">High</option><option value="medium">Medium</option><option value="low">Low</option></select></label><span id="result-count" role="status" aria-live="polite">{len(FINDINGS)} of {len(FINDINGS)} findings</span></div>{''.join(cards)}<p id="empty" class="no-results" hidden>No findings match these filters.</p></section>
<section id="remediation"><div class="eyebrow">Recommended order</div><h2>Close the tenant boundary, then bound the workload.</h2><div class="roadmap"><div><span class="phase">01 / BIND NAMES, KEYS AND SECRETS TO SPACES</span><h3>Make ownership a write-time check</h3><p>Reject ACME hostnames, ingress hostnames and DNS labels that another space already owns, and deliver ACME keys only to the owning space. Require view on cross-deployment mount sources. Run port forwards through the reservation and claim machinery and restrict the DNAT destination. Scope rotation writes and refusal messages to what the caller can see.</p><p class="small">RR-03, RR-05, RR-11, RR-15, RR-20 · owners: PKI, deployments, ingress plan, network</p></div><div><span class="phase">02 / BOUND THE WORKLOAD AND THE BUILD</span><h3>Give workloads what builds already have</h3><p>Apply the default seccomp profile, drop CAP_NET_RAW, and give workloads a memory, CPU and pids budget with node defaults, a spec override and a bound on /dev/shm. Add a per-node log byte budget. Deny private ranges from build egress and treat host-network mode as host-administrator trust.</p><p class="small">RR-01, RR-02, RR-04, RR-19, RR-22 · owners: runtime, network, log storage</p></div><div><span class="phase">03 / IDENTITY LIFECYCLE AND EDGES</span><h3>Revocation, deadlines, re-authentication</h3><p>Reissue the primary leaf, introduce a serial denylist and short workload leaves, enforce WireGuard key uniqueness, put header and handshake deadlines on the three public listeners, cap concurrent password hashing, replace passkeys in place, verify the agent approval code, require fresh authentication for enrolment and rotation, and fix wildcard templates on upgrade.</p><p class="small">OD-09, OD-10, OD-12, RR-07, RR-08, RR-09, RR-10, RR-21, RR-24 · owners: PKI, API, authentication</p></div></div><p class="dependency-note">The remaining items are bounded or hygiene work that can ride with the nearest owner: OD-02, OD-06, OD-11, OD-13, RR-06, RR-12, RR-13, RR-14, RR-16, RR-17, RR-18, RR-23, RR-25 and RR-26. Dependency bumps are cheap and independent of all of the above: golang.org/x/text v0.39.0, aws-sdk-go-v2/service/s3 v1.97.3, google.golang.org/grpc v1.82.1, and vite with the tailwind plugin moved to devDependencies.</p></section>
<section id="dependencies" class="dependencies"><div class="eyebrow">Dependency triage</div><h2>Three reachable advisories remain; the runtime is current.</h2><p>govulncheck v1.8.0 scanned the host and Linux/amd64 targets with Go 1.27.0 on 13 September 2026. <code>go.mod</code>, <code>go.sum</code> and <code>pnpm-lock.yaml</code> are byte-identical between commit {COMMIT[:7]} and {RECHECK[:7]}, so the scans stand for the re-check. Both targets report <strong>{len(symbol_ids)} advisories with symbol-level traces</strong> and {len(seen)} at module level. The audit's traces for golang.org/x/net (GO-2026-4918, the basis of OD-08) and the containerd client library are gone: x/net moved from v0.52.0 to v0.55.0 and containerd/v2 from v2.3.1 to v2.3.5.</p><p class="dependency-note">Runtime inventory: <strong>bundled containerd 2.3.5</strong> and <strong>bundled runc 1.5.1</strong>, pinned in <code>backend/lib/runtimebin</code> and reconciled at every agent start. Both are the newest stable releases as of this review; the September containerd advisories are fixed in 2.3.5 or confined to the CRI plugin, which OpenDeploy does not use.</p>
<div class="table-wrap"><table><thead><tr><th>Go advisory</th><th>Module / reported fixed version</th><th>Trace</th><th>OpenDeploy applicability</th></tr></thead><tbody>{''.join(go_rows)}</tbody></table></div>
<details><summary>Frontend advisory inventory — {sum(npm_counts.values())} alerts <span>{npm_counts.get('high',0)} high · {npm_counts.get('moderate',0)} moderate · build tree</span></summary><p class="dependency-note">Unchanged from 11 September. Every path runs through vite, postcss, nanoid or yaml, reached via vite and the tailwind vite plugin. Nothing in the built bundle served to browsers is affected. <code>pnpm audit --prod</code> reports the same set because <code>@tailwindcss/vite</code> and <code>tailwindcss</code> sit in <code>dependencies</code> rather than <code>devDependencies</code>.</p><div class="table-wrap"><table><thead><tr><th>Package / installed</th><th>Advisory</th><th>Upstream severity</th><th>Reported patched range</th></tr></thead><tbody>{''.join(npm_rows)}</tbody></table></div></details></section>
<section id="coverage"><div class="eyebrow">Scope and verification</div><h2>What was rerun, and what still needs a Linux host.</h2><h3>Audit reproductions rerun against HEAD</h3><p class="small">The 11 September overlay tests were run unchanged, followed by two corrected checks, most recently against commit {RECHECK[:7]} on {RECHECK_DATE}. A passing audit test means the audited behaviour is still present.</p><div class="table-wrap"><table><thead><tr><th>Test</th><th>Behaviour</th><th>Result</th></tr></thead><tbody>{''.join(repro_rows)}</tbody></table></div><div class="checks" style="margin-top:22px"><div><h3>Checks completed</h3><ul><li><strong>{go_passed} backend packages passed</strong> with <code>go test -count=1 ./...</code> at {RECHECK[:7]}; {go_failed} failed and {go_untested} have no tests.</li><li><strong>{fe_passed} frontend tests passed</strong> at {RECHECK[:7]}, {fe_failed} failures and {fe_skipped} skips.</li><li><strong>govulncheck</strong> host and Linux/amd64 scans completed and were triaged above.</li><li><strong>pnpm audit</strong> full and production scans completed; inventories preserved.</li><li><strong>Runtime advisories</strong> for containerd and runc checked against upstream releases on the review date.</li></ul><p class="small">Reproductions use synthetic values, temporary databases and loopback HTTP. Nothing started a workload, mounted a host path or contacted a production system.</p></div><div><h3>Controls verified in the reviewed paths</h3><ul><li>Nix builds run in a containerd container with the default seccomp profile, default capabilities, an egress-only network, a per-repository store, memory, CPU and pids limits, and an explicitly constructed environment. Layer digests are verified before import and store paths are confined to the store root.</li><li>Host mounts and host networking require <code>use_host_mounts</code> and <code>use_host_network</code>, held by cluster_admin only; the gate covers create, v2 update including the saved spec, space moves and agent sessions. Host mount validation rejects protected roots, their parents and <code>/</code>.</li><li>Enrollment hellos must carry a CSR whose public key hashes to the identifier, and hellos for member identifiers are rejected. <code>X-Forwarded-For</code> is no longer trusted anywhere.</li><li>The GitHub token is injected as a github.com-scoped header at fetch time, never written to .git/config or argv, and is applied to registries only at the ghcr.io token endpoint; the audit's PAT-forwarding concern in OD-11 does not hold.</li><li>Rate limits key on the socket address; bearer tokens travel in a header, not a cookie, so there is no CSRF surface.</li></ul></div></div>
<div class="checks" style="margin-top:22px"><div><h3>Limits and follow-up work</h3><ul><li>Tests ran on macOS. Mount confinement, nftables behaviour, WireGuard peer collapse and container escapes need the Linux VM harness; RR-07 in particular rests on WireGuard's documented per-key peer identity rather than an executed test.</li><li>The audit's earlier "follow-up" list is now findings: host-network policy (RR-04), resource budgets (RR-01), passkey re-authentication (RR-10), backup authenticity (RR-14) and log volume (RR-19).</li><li>No production host, traffic or database was inspected. Whether any operator-authored wildcard rule template exists on a live cluster (RR-21) must be checked there.</li><li>Ingress path handling was reviewed by reading the router; no fuzzing of encoded or dot-segment paths was run.</li></ul></div><div><h3>How to read the identifiers</h3><p>OD-nn keep the 11 September audit's numbers so the two reports can be read side by side; where an OD item narrowed, its previous severity is shown in the finding header. RR-01 to RR-19 are the items raised in the 12 September assessment, in area order: workload isolation, networking, authentication and PKI, then data and supply chain. RR-20 to RR-26 were found during this re-review.</p><p class="small">Three items from the 12 September list are closed and appear only in the closed table. Three changed severity and carry both values: RR-04 narrowed once host networking was gated, and RR-10 and RR-11 were raised on closer reading.</p></div></div>
<h3 style="margin-top:28px">Reproduction and evidence</h3><p>Rerun the audit's bounded checks from the repository root; a PASS means the behaviour is still present:</p><pre><code>python3 docs/security-reviews/2026-09-13-evidence/reproduce.py</code></pre><p class="small">The script imports the 11 September tests unchanged, adds the two corrected checks, injects them with <code>-overlay</code> and leaves application source unchanged. Rebuild this page with <code>python3 docs/security-reviews/2026-09-13-evidence/build_report.py</code>.</p><div class="evidence-links">{''.join(f'<a href="2026-09-13-evidence/{name}">{label}</a>' for name, label in [('findings_data.py', 'Finding data'), ('reproduction-results.txt', 'Audit reproductions against HEAD'), ('od-04-closure-results.txt', 'OD-04 closure check'), ('go-tests.txt', 'Go test results'), ('frontend-tests.txt', 'Frontend test results'), ('govulncheck-host-summary.json', 'Go/host scan summary'), ('govulncheck-linux-summary.json', 'Go/Linux scan summary'), ('pnpm-audit.json', 'Raw frontend audit'), ('govulncheck-host.json.gz', 'Raw Go/host scan (gzip)'), ('govulncheck-linux.json.gz', 'Raw Go/Linux scan (gzip)'), ('manifest.json', 'Evidence manifest')])}</div></section>
</main><footer class="shell">Prepared by Claude Code from the local OpenDeploy repository. Review date: 13 September 2026; status updated {RECHECK_DATE} at commit {RECHECK[:12]}. Findings describe commit {COMMIT[:12]} and stated assumptions; no remediation was performed as part of this review. This HTML uses no external scripts, fonts or analytics.</footer><script>{JS}</script></body></html>'''

OUT.write_text(document)
(EVIDENCE / "findings.json").write_text(json.dumps({"commit": COMMIT, "baseline": BASELINE, "date": "2026-09-13", "recheck_commit": RECHECK, "recheck_date": "2026-09-16", "release": RELEASE, "findings": FINDINGS, "closed": CLOSED, "accepted": ACCEPTED}, indent=2) + "\n")
print(f"Wrote {OUT} ({len(document.encode()):,} bytes)")
