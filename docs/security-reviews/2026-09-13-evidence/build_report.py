#!/usr/bin/env python3
"""Build the security re-review HTML from finding data and source excerpts.

The page was first built on 13 September 2026 against 2ea3af1, updated on
16 September (60d73bc), on 22 November (ef1f3508) and on 23 November, when three
findings were fixed in commits ca3ff94 to 73f163c. Excerpts of open
findings are read from the re-check commit; closed and accepted rows link to
the commit they were recorded against. A source marked FIX_COMMIT is read from
that commit and linked to the local file, because the commit is not pushed.
"""
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
PREVIOUS_RECHECK = "60d73bcb152cf52a4650b394d9ff505dc71f63cc"
RECHECK = "ef1f3508acdb7478f13fa695e7f02a3de8e4046f"
RECHECK_DATE = "22 November 2026"
RECHECK_ISO = "2026-11-22"
UPDATED_DATE = "23 November 2026"
UPDATED_ISO = "2026-11-23"
RELEASE = "v0.0.617"
REPO = "https://github.com/jptrs93/opsagent/blob/"
OUT = EVIDENCE.parent / "2026-09-13-opendeploy-security-re-review.html"
e = html.escape

from findings_data import FINDINGS, CLOSED, ACCEPTED, RECHECK_COMMIT, FIX_COMMIT, FIX_DATE  # noqa: E402

assert RECHECK_COMMIT == RECHECK
assert FIX_DATE == UPDATED_DATE

SEVERITY_ORDER = {"Critical": 0, "High": 1, "Medium": 2, "Low": 3}
ORIGIN_LABEL = {
    "audit": "11 Sep audit",
    "review": "12 Sep assessment",
    "new": "13 Sep re-review",
    "recheck": "New on 22 Nov",
}


FIX_COMMIT_TAG = f'<span class="worktree">{FIX_COMMIT[:7]}</span>'


def source_link(path, line, commit=None, tag=True):
    if commit == FIX_COMMIT:
        assert (ROOT / path).is_file(), path
        return f'<a href="../../{e(path)}">{e(path)}:{line}</a>' + (" " + FIX_COMMIT_TAG if tag else "")
    base = REPO + (commit or COMMIT) + "/"
    return f'<a href="{e(base+path)}#L{line}" target="_blank" rel="noopener noreferrer">{e(path)}:{line}</a>'


def excerpt(path, first, last, where=RECHECK):
    lines = subprocess.check_output(["git", "show", f"{where}:{path}"], cwd=ROOT, text=True).splitlines()
    code = "\n".join(f"{i:4}  {lines[i-1]}" for i in range(first, min(last, len(lines)) + 1))
    return f'<div class="excerpt">{source_link(path, first, where)}<pre><code>{e(code)}</code></pre></div>'


def refs(items):
    return " · ".join(f'<a href="{e(url)}" target="_blank" rel="noopener noreferrer">{e(title)}</a>' for title, url in items)


def badge(sev):
    return f'<span class="badge {sev.lower()}">{e(sev)}</span>'


def ids(*wanted):
    open_ids = {f["id"] for f in FINDINGS}
    missing = [i for i in wanted if i not in open_ids]
    assert not missing, f"roadmap names findings that are not open: {missing}"
    return ", ".join(wanted)


ordered = sorted(FINDINGS, key=lambda f: (SEVERITY_ORDER[f["severity"]], f["id"]))
counts = collections.Counter(f["severity"] for f in FINDINGS)
by_origin = collections.Counter(f["origin"] for f in FINDINGS)

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
    recheck = f'<p class="recheck"><strong>Re-check, {RECHECK_DATE}.</strong> {e(f["recheck"])}</p>' if f.get("recheck") else ""
    if f.get("update"):
        recheck += f'<p class="recheck"><strong>Update, {UPDATED_DATE}.</strong> {e(f["update"])}</p>'
    cards.append(f'''<article class="finding" id="{f['id']}" data-severity="{f['severity'].lower()}" data-origin="{f['origin']}">
    <div class="finding-meta">{badge(f['severity'])}<span>{f['id']} · {e(f['area'])}</span><span class="origin">{e(ORIGIN_LABEL[f['origin']])}</span>{prior}<span class="verification">{e(f['status'])}</span></div>
    <h3>{e(f['title'])}</h3><p class="impact">{e(f['impact'])}</p>{recheck}
    <div class="finding-body">{sections}</div>
    <details><summary>Source evidence <span>{e(f['cwe'])}</span></summary>{''.join(excerpt(*s) for s in f['sources'])}</details>
    {'<p class="references">'+refs(f['refs'])+'</p>' if f.get('refs') else ''}</article>''')

index_rows = "".join(
    f'<tr><td><a href="#{f["id"]}">{f["id"]}</a></td><td>{badge(f["severity"])}</td><td><a href="#{f["id"]}">{e(f["title"])}</a></td>'
    f'<td>{e(ORIGIN_LABEL[f["origin"]])}{("<small>was " + e(f["prior"]) + "</small>") if f.get("prior") else ""}</td><td>{e(f["status"])}</td></tr>'
    for f in ordered
)
closed_rows = "".join(
    f'<tr><td>{e(c["id"])}</td><td>{e(c["title"])}</td><td>{badge(c["prior"])}</td><td>{e(c["how"])}</td><td>{FIX_COMMIT_TAG + "<br>" if c.get("commit") == FIX_COMMIT else ""}{" ".join(source_link(p, l, c.get("commit"), tag=False) for p, l in c["sources"])}</td></tr>'
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
worktree_scan = json.loads((EVIDENCE / "govulncheck-worktree-summary.json").read_text())
worktree_left = sum(len(t["symbol_findings"]) + len(t["module_findings"]) for t in worktree_scan["targets"].values())
assert worktree_left == 0, "the scan of the fix commit is no longer clean; rewrite the dependency section"
PUBLIC = "Unauthenticated, on every HTTP/2 listener: the Web UI, the cluster listener and the netproxy ingress. "
triage = {
    "GO-2026-6603": PUBLIC + "A client exhausts server memory through Trailer headers. Fixed in Go 1.27.2.",
    "GO-2026-6617": PUBLIC + "A race in the HPACK encoder crashes the server process. Fixed in Go 1.27.2.",
    "GO-2026-6611": PUBLIC + "Repeated initial-window changes burn CPU. Also applies to OpenDeploy's HTTP/2 clients against a hostile registry or backend. Fixed in Go 1.27.2.",
    "GO-2026-6612": "HTTP/2 server streams: flow control is refunded twice, so a client can exceed the receive window. Fixed in Go 1.27.2 and x/net v0.60.0.",
    "GO-2026-6607": "TLS handshake on every listener and in outbound clients. The advisory concerns malformed ECH outer extension references; OpenDeploy configures no ECH keys and whether the affected parse runs without them was not determined. Fixed in Go 1.27.2.",
    "GO-2026-6859": "HTTP/1 servers that limit request bodies with MaxBytesReader, which the generated mux does. The crash is a race on the response header; whether any OpenDeploy handler meets its timing was not established. Fixed in Go 1.27.4.",
    "GO-2026-6613": "HTTP/1 server answering CONNECT with 2xx without hijacking. Reached through the netproxy ingress; whether a backend can make the proxy do so was not established. Fixed in Go 1.27.2.",
    "GO-2026-6605": "HTTP/1 client and ReverseProxy forwarding CONNECT with a body. Reached through the netproxy ingress; precondition not established. Fixed in Go 1.27.2.",
    "GO-2026-6610": "HTTP/2 client used by the netproxy to reach HTTP/2 backends. Response smuggling needs a hostile backend, which is a tenant workload, and a lax HTTP/1 client. Fixed in Go 1.27.2 and x/net v0.60.0.",
    "GO-2026-6608": "Memory limit bypass in MIME header parsing. Reached wherever HTTP headers are read, including the ingress server and outbound registry clients. Fixed in Go 1.27.2.",
    "GO-2026-6599": "html/template escaping inside JavaScript template literals. OpenDeploy's templates are static and written by the project. Fixed in Go 1.27.2.",
    "GO-2026-6600": "html/template escaping around the yield keyword. OpenDeploy's templates are static and written by the project. Fixed in Go 1.27.2.",
    "GO-2026-6604": "os.Root on Windows only. OpenDeploy ships for Linux. Fixed in Go 1.27.2.",
    "GO-2026-6809": "crypto/x509 gains a denylist of compromised intermediates. Affects every chain verification, including passkey attestation and outbound TLS. Fixed in Go 1.27.3.",
    "GO-2026-6597": "Image pull amplification through a crafted OCI index. Applies to the client library and to the bundled containerd 2.3.5 daemon; a deployer triggers it by pointing a deployment at a registry they control. Fixed in containerd 2.3.6; 2.3.7 is current.",
    "GO-2026-6686": "Unbounded parsing of user and group database files, reached from the build container path in the containerd client. Bump moby/sys/user to v0.4.1.",
    "GO-2026-6865": "Panic on two-block frames in klauspost/compress. The traces run through the response stream writer, which compresses data OpenDeploy produced; no route was found that decodes attacker-supplied frames with the affected code. Bump to v1.20.2.",
    "GO-2026-6888": "Excessive allocation when decompressing crafted brotli streams. The traces are interface dispatch only; brotli is a transitive dependency of the parquet library, and OpenDeploy writes its log and metrics files with zstd. Bump to v1.2.7.",
    "GO-2026-6348": "gRPC memory exhaustion through fragmented DATA frames. OpenDeploy uses gRPC only as a client to the local containerd socket. Bump to v1.83.1.",
    "GO-2026-6061": "gRPC xDS RBAC and HTTP/2 server transport. OpenDeploy uses gRPC only as a client to the local containerd socket; no gRPC server or xDS. Bump to v1.83.1.",
    "GO-2026-5970": "golang.org/x/text iterator loop on invalid input. Reachable symbol; input path is hostnames and labels handled by the IDNA and normalisation code. Unchanged since September. Bump to v0.39.0.",
    "GO-2026-5764": "AWS SDK EventStream decoder panic. Reachable through the S3 backup client; the attacker would need to control the S3 endpoint's responses. Unchanged since September. Bump service/s3 to v1.97.3.",
}
go_rows = []
seen = set()
all_findings = host["module_findings"] + linux["module_findings"]
all_findings.sort(key=lambda f: (f["id"] not in symbol_ids, f["id"]))
for f in all_findings:
    if f["id"] in seen:
        continue
    seen.add(f["id"])
    level = "symbol" if f["id"] in symbol_ids else "module only"
    go_rows.append(
        f'<tr><td><a href="https://pkg.go.dev/vuln/{f["id"]}">{f["id"]}</a><small>{e(f["summary"])}</small></td>'
        f'<td><code>{e(f["module"])}</code><small>{e(f.get("version") or "")} → {e(f.get("fixed_version") or "no fix listed")}</small></td>'
        f'<td>{e(level)}</td><td>{e(triage.get(f["id"], "Module-level match only; no vulnerable symbol reached from OpenDeploy code."))}</td></tr>'
    )
stdlib_symbol = len({f["id"] for f in all_findings if f["id"] in symbol_ids and f["module"] == "stdlib"})
untriaged = sorted(symbol_ids - set(triage))
assert not untriaged, f"symbol-level advisories without a triage note: {untriaged}"
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
REPRO = [
    ("TestAuditRegistryRealmCallsLoopback", "open", "OD-11 registry realm reaches loopback (11 September test, unchanged)"),
    ("TestAuditRegistryPaginationForwardsBearerToAnotherOrigin", "open", "OD-11 bearer forwarded to another origin (11 September test, unchanged)"),
    ("TestRecheckMountParentRejectedSymlinkAccepted", "open", "OD-02 residual: protected roots and their parents are rejected, a symlink into one passes the lexical validator"),
    ("TestRecheckUserUpdateMintsSignInLinkForAnAdmin", "fixed", "RR-27 reproduction of 22 November, kept unchanged: a user holding only user:update issues and redeems a sign-in link for a cluster admin. It passed at ef1f350 and is expected to fail after the fix"),
    ("TestRecheckPortForwardClaimsRejectedAtWriteTime", "closed", "RR-05 (part) a second forward on a taken TCP or UDP port and a forward on the WireGuard port are rejected at write time; a space admin is denied a forward on port 22"),
    ("TestRecheckSignInLinkRefusedWithoutAccessCreate", "closed", "RR-27 a user holding every verb on the user type and no access management is refused a link for a cluster admin; an admin can still issue one that redeems"),
    ("TestRecheckEpochStatusClockBreaksTheOpeningSnapshot", "open", "RR-29 one epoch-clock status report makes the admin's opening snapshot fail to encode"),
    ("TestRecheckAgentCanBindDeniedSecret", "accepted", "OD-05 an agent denied reveal binds the secret into a workload it controls. Accepted by design, see AP-1"),
    ("TestRecheckPasskeySaveReplacesByCredentialID", "closed", "OD-12 (part) saving one credential twice leaves one row with the latest counter"),
    ("TestRecheckSpaceIDNotReusedAndDeleteRefusedWhileReferenced", "closed", "RR-13 a deleted space's id is not reissued; a space holding a secret or named by a grant cannot be deleted"),
    ("TestRecheckSpaceAdminHostMountDenied", "holds", "OD-02 (part) space_admin is denied a custom host mount even for an unprotected path"),
    ("TestRecheckOtherSpaceCertificateNameRejectedAtCreate", "holds", "OD-04 another space's .internal name is rejected at create"),
]
VERDICT = {
    "open": ("Still reproduces", "open", "No longer reproduces", "fixed"),
    "fixed": ("Still reproduces", "open", "No longer reproduces", "fixed"),
    "accepted": ("Reproduces; accepted", "open", "No longer reproduces", "fixed"),
    "closed": ("Closed", "fixed", "Closure check failed", "open"),
    "holds": ("Still closed", "fixed", "Regressed", "open"),
}
repro_rows = []
for name, kind, meaning in REPRO:
    assert name in repro, f"{name} missing from reproduction-results.txt"
    passed = f"--- PASS: {name}" in repro
    assert passed or f"--- FAIL: {name}" in repro, f"{name} neither passed nor failed"
    yes, yes_cls, no, no_cls = VERDICT[kind]
    verdict, cls = (yes, yes_cls) if passed else (no, no_cls)
    repro_rows.append(f'<tr><td><code>{e(name)}</code></td><td>{e(meaning)}</td><td><span class="badge {cls}">{verdict}</span></td></tr>')
repro_open = sum(1 for n, k, _ in REPRO if k in ("open", "accepted") and f"--- PASS: {n}" in repro)
repro_closed = sum(1 for n, k, _ in REPRO if k in ("closed", "holds") and f"--- PASS: {n}" in repro)
repro_gone = sum(1 for n, k, _ in REPRO if k == "fixed" and f"--- FAIL: {n}" in repro)
assert repro_open + repro_closed + repro_gone == len(REPRO), "a reproduction or closure check did not give the expected result"

CSS = (EVIDENCE / "_style.css").read_text()
JS = (EVIDENCE / "_script.js").read_text()
R7, C7, P7, F7 = RECHECK[:7], COMMIT[:7], PREVIOUS_RECHECK[:7], FIX_COMMIT[:7]

document = f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><meta name="referrer" content="no-referrer"><title>OpenDeploy — Security Re-review · updated {UPDATED_DATE}</title><style>{CSS}</style></head>
<body><a class="skip" href="#findings">Skip to findings</a><div class="topbar"><div class="shell"><div class="brand">OPENDEPLOY / SECURITY REVIEW</div><span>Re-review of the 11 September audit · 13 September 2026 · status updated {UPDATED_DATE}</span></div></div>
<header class="hero shell"><div class="eyebrow">Security re-review / updated November 2026</div><h1>The host boundary is largely closed. The tenant boundary is not.</h1><p class="lede">Updated {UPDATED_DATE}. The full re-check ran on {RECHECK_DATE} at commit {R7}. Two findings had closed since September: space ids are no longer reused and a space in use cannot be deleted (RR-13), and passkey logins no longer append duplicate credentials (half of OD-12). The other 31 were still open in the rewritten code. Eleven were added: seven in code that did not exist in September (per-user sign-in, the write log and event stream, assets stored by key), three in older code, and one for dependency advisories published since. On {UPDATED_DATE} three High items were fixed in commits after {R7}, not yet released: issuing a sign-in link now needs access management (RR-27, closed), the Go toolchain, modules and bundled runtime are bumped (RR-28, open until a release carries the fix), and port forwards can no longer take a platform port, a low port or another deployment's port (RR-05, narrowed to Medium). The tenant-boundary items from September remain the largest group; RR-05 is the first of them to move.</p><div class="meta"><span>Reviewed 13 September 2026 · Australia/Sydney</span><span>Commit <code>{COMMIT[:12]}</code></span><span>Baseline <code>{BASELINE[:12]}</code></span><span>Re-checked {RECHECK_DATE} at <code>{RECHECK[:12]}</code> ({RELEASE} plus unreleased sign-in work)</span><span>Three fixes on {UPDATED_DATE}, committed up to {F7}, unreleased</span><span>Source review, executed reproductions, dependency scans</span></div><div class="scorecard">{stats}<div class="stat-note"><strong>{len(FINDINGS)} open findings</strong>{by_origin['audit']} carried from the 11 September audit, {by_origin['review']} from the 12 September assessment, {by_origin['new']} from the 13 September re-review, {by_origin['recheck']} raised on {RECHECK_DATE}. Every item was re-verified against commit {R7}; anything closed has moved to the closed table. RR-05, RR-27 and RR-28 reflect commit {F7} of {UPDATED_DATE}.</div></div></header>
<nav class="nav" aria-label="Report sections"><div class="shell"><a href="#assessment">Assessment</a><a href="#closed">Closed</a><a href="#findings">Open findings</a><a href="#remediation">Remediation</a><a href="#dependencies">Dependencies</a><a href="#coverage">Validation</a><button id="print" type="button">Print / save PDF</button></div></nav>
<main class="shell"><section id="assessment"><div class="intro-grid"><div class="callout"><strong>Bind identity to the space, then bound the workload.</strong><p>The two Critical paths from September 11 are closed or gated to cluster administrators, and those closures still hold after the data-model rewrite. The remaining High items from September share one shape: something a space-scoped operator can name, claim or bind is honoured cluster-wide without checking that the space owns it. Host ports were one of those; that part was fixed on {UPDATED_DATE} (RR-05). The two High items added in November were different, and both were fixed the next day. One verb on the user type signed in as any account on the unreleased branch; that is closed (RR-27). The toolchain of the published release carries unauthenticated denial-of-service advisories on the public listeners; that stays open until a release carries the fix (RR-28).</p><div class="flow" aria-label="Observed escalation path"><span>Space-scoped write</span><b aria-hidden="true">→</b><span>Cluster-wide name, port or key</span><b aria-hidden="true">→</b><span>Another tenant's traffic or credentials</span></div></div><div class="scope"><p><strong>Scope.</strong> Every finding of the 11 September audit and the 12 and 13 September reviews, re-verified against the Go backend, frontend, API contract and installer at commit {RECHECK[:12]}, plus a review of the code added since: per-user passwords, enrol and recovery links and root sessions; the write log, the browser event stream and its visibility rules; every route of the public API; the primary to worker boundary and the node publisher; secret seals; and assets stored by key.</p><p><strong>Method.</strong> Source review with file and line evidence; ported overlay reproductions executed against HEAD; the full backend and frontend test suites; govulncheck for host and Linux targets; pnpm audit; the toolchain of the published {RELEASE} binary. Linux mounts, escapes, kernel behaviour and load were not executed.</p><p><strong>Identifiers.</strong> OD-nn are the audit's original identifiers and keep their numbers. RR-01 to RR-26 were raised in September; RR-27 to RR-37 on {RECHECK_DATE}. Where a finding narrowed, the previous severity is shown beside the new one.</p></div></div>
<p class="small" style="margin-top:22px">Critical = plausible host/control-plane compromise from lesser deployment authority. High = major confidentiality, integrity or shared-service impact under stated prerequisites. Medium = bounded, conditional or availability/hardening exposure. Low = hygiene or defence in depth. No CVSS scores were assigned.</p>
<h3 style="margin-top:26px">Status history</h3><div class="table-wrap"><table><thead><tr><th>Date</th><th>Commit</th><th>Open</th><th>What changed</th></tr></thead><tbody>
<tr><td>13 September 2026</td><td><code>{C7}</code></td><td>34</td><td>Re-review of the 11 September audit: 8 closed, 1 accepted by design, 7 new.</td></tr>
<tr><td>16 September 2026</td><td><code>{P7}</code> (v0.0.609)</td><td>33</td><td>OD-07 closed by node eviction. No other code path touched.</td></tr>
<tr><td>{RECHECK_DATE}</td><td><code>{R7}</code> ({RELEASE} + branch)</td><td>43</td><td>RR-13 closed; OD-12 narrowed from Medium to Low with its duplicate-row half closed; 31 others unchanged in substance; RR-27 to RR-37 added. No severity changed for a reason other than a verified fix.</td></tr>
<tr><td>{UPDATED_DATE}</td><td><code>{F7}</code>, unreleased</td><td>{len(FINDINGS)}</td><td>RR-27 closed by gating link issuance on access management. RR-28 fixed in source and left open until a release carries it. RR-05 narrowed from High to Medium: forwards are checked against other forwards and platform listeners at write time, ports below 1024 need a new host permission, and forward rules skip loopback. No other finding was re-reviewed. The fixes are commits ca3ff94, 12f2d8c and fa23e93, with the upgrade notes in {F7}; none is pushed or released. The VM harness passed on this code.</td></tr>
</tbody></table></div>
<h2 style="margin-top:30px">Open finding index</h2><div class="table-wrap"><table><thead><tr><th>ID</th><th>Severity</th><th>Finding</th><th>Origin</th><th>Evidence level</th></tr></thead><tbody>{index_rows}</tbody></table></div></section>
<section id="closed"><div class="eyebrow">Closed since 11 September</div><h2>What the intervening commits fixed.</h2><p class="small">Each closure was confirmed by reading the current code and, where a reproduction exists, by running it. Fixed code does not retire credentials or state exposed before the fix. The first two rows were fixed on {UPDATED_DATE} in commits fa23e93 and ca3ff94; the commits are not pushed, so the rows link to the local files. The next two closed between 16 September and {RECHECK_DATE} and link to commit {RECHECK[:12]}; the rest link to the commit they were recorded against. All earlier closures were re-checked on {RECHECK_DATE} after the data-model rewrite and still hold: builds run in the locked-down container with a constructed environment, the host verbs gate create, update and space moves, enrollment identifiers are bound to the CSR key, evicted nodes are refused on every cluster route, and another space's certificate name is rejected. Newer advisories apply to the runtime pins behind OD-03 and the x/net version behind OD-08 as they stand at that commit; those are tracked as RR-28 rather than by reopening the old items.</p><div class="table-wrap"><table><thead><tr><th>ID</th><th>Finding</th><th>Was</th><th>How it closed</th><th>Evidence</th></tr></thead><tbody>{closed_rows}</tbody></table></div>
<h2 style="margin-top:30px">Accepted by design</h2><p class="small">Behaviour that is present and reproducible but recorded as documented authority in <code>docs/engineering/auth.md</code> under <em>Accepted positions</em>. Per <code>security-severity.md</code> such items are Informational; a future review that disagrees should argue against the position rather than reopen the finding. The behaviour was reproduced again on {RECHECK_DATE}; the evidence links point at commit {COMMIT[:12]}.</p><div class="table-wrap"><table><thead><tr><th>ID</th><th>Finding</th><th>Was</th><th>Position</th><th>Why it is accepted</th><th>Evidence</th></tr></thead><tbody>{accepted_rows}</tbody></table></div></section>
<section id="findings"><div class="eyebrow">Evidence-led review</div><h2>Open findings</h2><p class="small">Source links and embedded excerpts point to commit {RECHECK[:12]}, so the report reads offline against the current tree. Findings raised in September keep their original wording where it is still accurate; each carries a dated re-check note saying what was found on {RECHECK_DATE}, and statements that no longer held were corrected. Reproduced behaviour is distinguished from a full exploit. Recommendations have not been applied, with two exceptions: the fixes for RR-28 and for most of RR-05 are committed, and those two findings' excerpts are read from commit {F7} and marked so. Two findings apply only to the unreleased branch and say so under Prerequisites: RR-31 and RR-37.</p><div class="filters"><label>Search findings<br><input id="search" type="search" placeholder="Search a component, risk or finding ID…"></label><label>Severity<br><select id="severity"><option value="">All severities</option><option value="critical">Critical</option><option value="high">High</option><option value="medium">Medium</option><option value="low">Low</option></select></label><span id="result-count" role="status" aria-live="polite">{len(FINDINGS)} of {len(FINDINGS)} findings</span></div>{''.join(cards)}<p id="empty" class="no-results" hidden>No findings match these filters.</p></section>
<section id="remediation"><div class="eyebrow">Recommended order</div><h2>Gate the next release, close the tenant boundary, then bound the workload.</h2><div class="callout" style="margin-bottom:17px"><strong>Before the next release</strong><p>Two findings exist only on the unreleased sign-in branch and are cheapest to fix before it ships: move the Argon2 verification out of the commit and under the credential limiter, and carry the old password-login setting into require_passkey. A third, link issuance without access management, was fixed on {UPDATED_DATE} and is in the closed table. Two more affect the published {RELEASE}. The toolchain, module and runtime bumps are committed and passed the VM harness: release them. Worker status reports with a zero or implausible clock should be rejected.</p><p class="small">{ids("RR-31", "RR-37", "RR-28", "RR-29")} · owners: authentication, release engineering, cluster protocol</p></div><div class="roadmap"><div><span class="phase">01 / BIND NAMES, KEYS AND SECRETS TO SPACES</span><h3>Make ownership a write-time check</h3><p>Reject ACME hostnames, ingress hostnames and DNS labels that another space already owns, and deliver ACME keys only to the owning space. Require view on cross-deployment mount sources. Let an administrator reserve the host ports of services the platform does not know about; the rest of the port-forward work was done on {UPDATED_DATE}. Scope rotation writes and refusal messages to what the caller can see, and check a reference's space before reading the value behind it.</p><p class="small">{ids("RR-03", "RR-05", "RR-11", "RR-15", "RR-20", "RR-34")} · owners: PKI, deployments, ingress plan, network</p></div><div><span class="phase">02 / BOUND THE WORKLOAD, THE BUILD AND THE WORKER</span><h3>Give workloads what builds already have</h3><p>Apply the default seccomp profile, drop CAP_NET_RAW, and give workloads a memory, CPU and pids budget with node defaults, a spec override and a bound on /dev/shm. Add a per-node log byte budget. Deny private ranges from build egress and treat host-network mode as host-administrator trust. Bound what one worker can write to the primary's log, verify asset bytes against the recorded hash, and finalize terminating instances on a deadline.</p><p class="small">{ids("RR-01", "RR-02", "RR-04", "RR-19", "RR-22", "RR-30", "RR-33", "RR-36")} · owners: runtime, network, log storage, cluster protocol</p></div><div><span class="phase">03 / IDENTITY LIFECYCLE AND EDGES</span><h3>Revocation, deadlines, re-authentication</h3><p>Reissue the primary leaf, introduce a serial denylist and short workload leaves, enforce WireGuard key uniqueness, put header and handshake deadlines on the public listeners, cap concurrent password hashing, act on passkey clone warnings, verify the agent approval code, require fresh authentication for enrolment and rotation, end streams when their session ends, hide sibling sessions from agents, and fix wildcard rules on upgrade.</p><p class="small">{ids("OD-09", "OD-10", "OD-12", "RR-07", "RR-08", "RR-09", "RR-10", "RR-21", "RR-24", "RR-32", "RR-35")} · owners: PKI, API, authentication</p></div></div><p class="dependency-note">The remaining items are bounded or hygiene work that can ride with the nearest owner: {ids("OD-02", "OD-06", "OD-11", "OD-13", "RR-06", "RR-12", "RR-14", "RR-16", "RR-17", "RR-18", "RR-23", "RR-25", "RR-26")}.</p></section>
<section id="dependencies" class="dependencies"><div class="eyebrow">Dependency triage</div><h2>{len(symbol_ids)} reachable advisories in {RELEASE}, up from 3; none at {F7}.</h2><p><strong>At commit {R7}, {RECHECK_DATE}.</strong> govulncheck v1.8.0 scanned the host and Linux/amd64 targets with Go 1.27.0 against the 11 November vulnerability database. <code>go.sum</code> and <code>pnpm-lock.yaml</code> were byte-identical to September, so every new entry is an advisory published since against a version that was already pinned. Both targets reported <strong>{len(symbol_ids)} advisories with symbol-level traces</strong> and {len(seen)} at module level. {stdlib_symbol} of the symbol-level ones are in the Go standard library and are fixed by patch releases 1.27.2 to 1.27.4. The published {RELEASE} linux/amd64 binary reports <code>go1.27.1</code>, so they apply to the release as shipped. The reachable ones that matter are tracked as RR-28, and the table below is that scan.</p><p><strong>Commit {F7}, {UPDATED_DATE}.</strong> With <code>go.mod</code> requiring Go 1.27.4 and the modules bumped, the same scanner and database report <strong>no advisory at symbol or module level</strong> for host, Linux/amd64 and Linux/arm64, scanned with {e(worktree_scan["go_version"])}. The release workflow now asks <code>setup-go</code> for the latest 1.27 patch and runs govulncheck for the target architecture before the build, so a reachable advisory fails the release. That workflow change has not run in CI. <code>pnpm-lock.yaml</code> is unchanged, so the frontend inventory below still applies.</p><p class="dependency-note">Runtime inventory: {RELEASE} and commit {R7} bundle <strong>containerd 2.3.5</strong> and <strong>runc 1.5.1</strong>, pinned in <code>backend/lib/runtimebin</code> and reconciled at every agent start. Commit {F7} pins <strong>containerd 2.3.7</strong> and <strong>runc 1.5.3</strong>, the current releases, with checksums that match the ones published upstream. From the release notes: containerd 2.3.6 fixes GO-2026-6597 and masks <code>/proc/interrupts</code> and the thermal-throttle sysfs paths in new containers; 2.3.7 carries further security fixes and makes the default seccomp profile block <code>AF_ALG</code> and <code>AF_VSOCK</code> sockets, which applies to Nix build containers; runc 1.5.3 fixes a checkpoint and restore advisory, a feature OpenDeploy does not use. The new pins ran on the VM harness on {UPDATED_DATE}.</p>
<div class="table-wrap"><table><thead><tr><th>Go advisory</th><th>Module / reported fixed version</th><th>Trace</th><th>OpenDeploy applicability</th></tr></thead><tbody>{''.join(go_rows)}</tbody></table></div>
<details><summary>Frontend advisory inventory — {sum(npm_counts.values())} alerts <span>{npm_counts.get('high',0)} high · {npm_counts.get('moderate',0)} moderate · {npm_counts.get('low',0)} low · build tree</span></summary><p class="dependency-note">Fourteen on 11 September, {sum(npm_counts.values())} now, with an unchanged lockfile. Every path runs through vite, postcss, nanoid, source-map-js or yaml, reached via vite and the tailwind vite plugin. Nothing in the built bundle served to browsers is affected. <code>pnpm audit --prod</code> reports the same set because <code>@tailwindcss/vite</code> and <code>tailwindcss</code> sit in <code>dependencies</code> rather than <code>devDependencies</code>.</p><div class="table-wrap"><table><thead><tr><th>Package / installed</th><th>Advisory</th><th>Upstream severity</th><th>Reported patched range</th></tr></thead><tbody>{''.join(npm_rows)}</tbody></table></div></details></section>
<section id="coverage"><div class="eyebrow">Scope and verification</div><h2>What was rerun, and what still needs a Linux host.</h2><h3>Reproductions and closure checks executed against HEAD</h3><p class="small">The data-model rewrite changed the API the 11 September overlay tests were written against, so only the two registry tests still compile; they were run unchanged. The others were ported to the current API and joined by closure checks and by reproductions of two November findings. All ran against commit {R7} on {RECHECK_DATE} and were rerun on {UPDATED_DATE} against commit {F7}, which carries the fixes of that day: {repro_open} behaviours reproduce, {repro_closed} closures hold, and the RR-27 reproduction that passed on {RECHECK_DATE} no longer does.</p><div class="table-wrap"><table><thead><tr><th>Test</th><th>Behaviour</th><th>Result</th></tr></thead><tbody>{''.join(repro_rows)}</tbody></table></div><div class="checks" style="margin-top:22px"><div><h3>Checks completed</h3><ul><li><strong>{go_passed} backend packages passed</strong> with <code>go test -count=1 ./...</code> at commit {F7} with Go 1.27.4 on {UPDATED_DATE}; {go_failed} failed and {go_untested} have no tests. The same {go_passed} passed at {R7} on {RECHECK_DATE}.</li><li><strong>{fe_passed} frontend tests passed</strong> at commit {F7}, {fe_failed} failures and {fe_skipped} skips, as at {R7}.</li><li><strong>govulncheck</strong> host and Linux/amd64 scans at {R7} completed and were triaged above; host, Linux/amd64 and Linux/arm64 scans at {F7} found nothing.</li><li><strong>pnpm audit</strong> full and production scans completed; inventory preserved.</li><li><strong>Release binary</strong>: the {RELEASE} linux/amd64 asset was downloaded and its embedded toolchain and module versions read with <code>go version -m</code>.</li><li><strong>Runtime releases</strong> for containerd and runc checked against upstream on the re-check date; on {UPDATED_DATE} the four new pinned checksums were compared with the upstream checksum files and the release notes were read.</li><li><strong>Kernel behaviour of the port-forward rules</strong>: the network audit round trip and a loopback test were run as root in a Linux 6.12 container on {UPDATED_DATE}. Both pass, and the loopback test shows a host service timing out beside a forward without the exclusion.</li><li><strong>VM harness</strong>: the full end-to-end suite ran on {UPDATED_DATE} on three freshly built Lima VMs, on the working tree that became {F7}. All 1006 flow steps, the WireGuard checks and the network-policy kernel checks passed in 32 minutes. The nodes installed containerd 2.3.7 and runc 1.5.3, and the forwards the suite creates carried the loopback exclusion in the live nftables ruleset.</li></ul><p class="small">Reproductions use synthetic values, temporary databases and loopback HTTP. Nothing started a workload, mounted a host path or contacted a production system.</p></div><div><h3>Controls verified in the reviewed paths</h3><ul><li>Every route of the public API has an access check or is on the documented unenforced list; values, assets and folders are checked against the object's own space, and logs, metrics and run reports require <code>view_logs</code>.</li><li>The event stream is default-deny per connection, takes its snapshot and subscription atomically under the write lock, re-sends a filtered snapshot when grants or node allow-lists change, and strips sealed secrets, password and token hashes, passkey material and the master hash from snapshots, live mutations and write receipts. Key slots never leave.</li><li>Session and link tokens carry a 256-bit secret, only the SHA-256 is stored, comparison is constant-time, link redemption is single-use inside one commit with a uniform failure, and a link is never accepted as a bearer. A root session fails every grant check and reaches six routes.</li><li>Every cluster route maps the certificate CN to a member node; evicted and pending nodes are refused. Secret, config, asset and issued-TLS fetches are limited to references pinned by the caller's own non-final instances, status writes to its own instances, and system-space secrets are refused on the path workers use.</li><li>Each node's projection holds its own placements plus policy-reachable and global spaces, with the system space excluded. Storage keys are server-generated and S3 credentials stay on the primary.</li><li>Nix builds, host mounts, host networking, enrollment identifiers and the GitHub token handling are as recorded in September.</li></ul></div></div>
<div class="checks" style="margin-top:22px"><div><h3>Limits and follow-up work</h3><ul><li>Tests ran on macOS. Mount confinement, nftables behaviour, WireGuard peer collapse and container escapes need the Linux VM harness; RR-07 in particular rests on WireGuard's documented per-key peer identity rather than an executed test.</li><li>RR-30 and RR-31 are availability findings read from source; neither the write-log growth rate nor the lock hold time was measured.</li><li>The fixes of {UPDATED_DATE} are committed locally and neither pushed nor released. The changed release workflow has not run. Every node restarts containerd once when it takes a release with the new pins.</li><li>No end-to-end case covers the new permission or the collision refusal; they rest on unit tests and the closure check. One Linux-only unit test in <code>lib/network</code>, <code>TestTopologyValidationRequiresWireGuardTransport</code>, fails in a Linux container on commit {R7} as well as on {F7}; it is unrelated and was not investigated.</li><li>No production host, traffic or database was inspected. Whether any custom rule uses an all-verbs-excluding selector (RR-21) must be checked on each live cluster.</li><li>One candidate was not confirmed and is not listed as a finding: deployment update authorises the record it read before taking the write lock, so an update racing a space move could write back the old space. The width of that window was not established.</li><li>Observations below finding level, read from source and worth a decision: signing out clears the browser's copy of the token without revoking the session; a session opened by root for a user carries no durable marker of who opened it; the master password has no minimum length while user passwords need eight characters; a failed login logs the submitted username; and the enrol token travels in the URL path rather than a fragment.</li><li>Not reviewed this time: the restore and import write paths, the worker's application of the network map, and the netproxy beyond the tracked findings. Ingress path handling was read, not fuzzed.</li></ul></div><div><h3>How to read the identifiers</h3><p>OD-nn keep the 11 September audit's numbers so the two reports can be read side by side; where an OD item narrowed, its previous severity is shown in the finding header. RR-01 to RR-19 are the items raised in the 12 September assessment, RR-20 to RR-26 were found during the 13 September re-review, and RR-27 to RR-37 on {RECHECK_DATE}.</p><p class="small">Severity changes since the audit: RR-04 narrowed once host networking was gated, RR-10 and RR-11 were raised on closer reading, OD-02 and OD-12 narrowed when their main defect closed. RR-27 was rated High although its prerequisite, a custom grant conveying <code>user:update</code>, was unverified; under <code>security-severity.md</code> a control-plane takeover behind a difficult prerequisite stays High. It closed on {UPDATED_DATE}. RR-05 narrowed from High to Medium the same day, when everything but host services on ports of 1024 and above was fixed. RR-21 has the same shape and was rated Medium in September; that rating was left as recorded. RR-28 keeps its High rating while the published release is unchanged.</p></div></div>
<h3 style="margin-top:28px">Reproduction and evidence</h3><p>Rerun the bounded checks from the repository root; each log line says whether a PASS means reproduced, closed or still closed:</p><pre><code>python3 docs/security-reviews/2026-09-13-evidence/reproduce.py</code></pre><p class="small">The script injects its tests with <code>-overlay</code> and leaves application source unchanged. Rebuild this page with <code>python3 docs/security-reviews/2026-09-13-evidence/build_report.py</code>. The evidence files were regenerated on {RECHECK_DATE}; the 16 September versions are in git history, and <code>od-04-closure-results.txt</code> is kept as recorded on 13 September. On {UPDATED_DATE} the test results and the reproduction log were regenerated against commit {F7}. The two scan files of {RECHECK_DATE} describe commit {R7} and are kept, with the scan summary for {F7} beside them.</p><div class="evidence-links">{''.join(f'<a href="2026-09-13-evidence/{name}">{label}</a>' for name, label in [('findings_data.py', 'Finding data'), ('reproduction-results.txt', 'Reproductions and closure checks'), ('od-04-closure-results.txt', 'OD-04 closure check (13 Sep)'), ('go-tests.txt', 'Go test results'), ('frontend-tests.txt', 'Frontend test results'), ('govulncheck-host-summary.json', 'Go/host scan summary'), ('govulncheck-linux-summary.json', 'Go/Linux scan summary'), ('govulncheck-worktree-summary.json', 'Go scan of the fix commit (23 Nov)'), ('pnpm-audit.json', 'Raw frontend audit'), ('govulncheck-host.json.gz', 'Raw Go/host scan (gzip)'), ('govulncheck-linux.json.gz', 'Raw Go/Linux scan (gzip)'), ('manifest.json', 'Evidence manifest')])}</div></section>
</main><footer class="shell">Prepared by Claude Code from the local OpenDeploy repository. Review date: 13 September 2026; re-checked {RECHECK_DATE} at commit {RECHECK[:12]}; status updated {UPDATED_DATE}. Open findings describe commit {RECHECK[:12]} and stated assumptions. RR-27, RR-28 and most of RR-05 were fixed on {UPDATED_DATE} at the owner's request, in commits ca3ff94 to {F7}, and are recorded as such; no other remediation was performed. This HTML uses no external scripts, fonts or analytics.</footer><script>{JS}</script></body></html>'''

OUT.write_text(document)
(EVIDENCE / "findings.json").write_text(json.dumps({"commit": COMMIT, "baseline": BASELINE, "date": "2026-09-13", "recheck_commit": RECHECK, "recheck_date": RECHECK_ISO, "updated_date": UPDATED_ISO, "fix_commit": FIX_COMMIT, "fixed_after_recheck": ["RR-27", "RR-28", "RR-05"], "previous_recheck_commit": PREVIOUS_RECHECK, "release": RELEASE, "findings": FINDINGS, "closed": CLOSED, "accepted": ACCEPTED}, indent=2) + "\n")
print(f"Wrote {OUT} ({len(document.encode()):,} bytes)")
