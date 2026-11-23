#!/usr/bin/env python3
"""Write manifest.json: commit, commands, headline results and sha256 of every evidence file."""
import hashlib, json, pathlib, platform, re, subprocess
HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[2]
go = subprocess.check_output(["go", "version"], text=True, cwd=ROOT / "backend").split()[2]
repro = (HERE / "reproduction-results.txt").read_text()
gotests = (HERE / "go-tests.txt").read_text()
fe = (HERE / "frontend-tests.txt").read_text()
host = json.loads((HERE / "govulncheck-host-summary.json").read_text())
linux = json.loads((HERE / "govulncheck-linux-summary.json").read_text())
worktree = json.loads((HERE / "govulncheck-worktree-summary.json").read_text())
audit = json.loads((HERE / "pnpm-audit.json").read_text())
findings = json.loads((HERE / "findings.json").read_text())
files = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(HERE.iterdir()) if p.is_file() and p.name != "manifest.json"}
manifest = {
    "review_date": "2026-09-13",
    "timezone": "Australia/Sydney",
    "commit": findings["commit"],
    "baseline_commit": findings["baseline"],
    "recheck_date": findings.get("recheck_date"),
    "recheck_commit": findings.get("recheck_commit"),
    "updated_date": findings.get("updated_date"),
    "fix_commit": findings.get("fix_commit"),
    "fixed_after_recheck": findings.get("fixed_after_recheck"),
    "fix_note": "Fixes of the updated date are commits ca3ff94 to 73f163c, unpushed and unreleased. Test results, reproductions and the worktree scan file describe 73f163c; the host and linux scan files describe the recheck commit. The VM e2e harness passed on this code on the updated date.",
    "release": findings.get("release"),
    "release_binary_toolchain": "go1.27.1",
    "platform": platform.platform(),
    "go": go,
    "commands": {
        "go_tests": "go test -count=1 ./... (backend/)",
        "frontend_tests": "pnpm test (frontend/)",
        "frontend_audit": "pnpm audit --json; pnpm audit --prod --json (frontend/)",
        "host_scan": "govulncheck -json ./... (backend/; v1.8.0 installed to a scratch GOBIN)",
        "linux_scan": "GOOS=linux GOARCH=amd64 govulncheck -json ./... (backend/)",
        "worktree_scan": "govulncheck -json ./... for host, GOOS=linux GOARCH=amd64 and GOOS=linux GOARCH=arm64 (backend/, fix commit), summarised per target into govulncheck-worktree-summary.json",
        "scan_summaries": "python3 docs/security-reviews/2026-09-13-evidence/summarize_govulncheck.py <raw.json> <summary.json>",
        "release_toolchain": "gh release download v0.0.617 (linux-amd64 asset); go version -m <binary>",
        "reproductions": "python3 docs/security-reviews/2026-09-13-evidence/reproduce.py",
        "report": "python3 docs/security-reviews/2026-09-13-evidence/build_report.py",
        "validation": "node docs/security-reviews/2026-09-13-evidence/validate_report.cjs",
    },
    "results": {
        "open_findings": len(findings["findings"]),
        "closed_since_audit": len(findings["closed"]),
        "accepted_by_design": len(findings["accepted"]),
        "go_packages_passed": len(re.findall(r"^ok\s", gotests, re.M)),
        "go_packages_failed": len(re.findall(r"^FAIL\s", gotests, re.M)),
        "frontend_tests_passed": int(re.search(r"^ℹ pass (\d+)", fe, re.M).group(1)),
        "frontend_tests_failed": int(re.search(r"^ℹ fail (\d+)", fe, re.M).group(1)),
        "audit_reproductions_still_passing": len(re.findall(r"^--- PASS: TestAudit", repro, re.M)),
        "audit_reproductions_no_longer_passing": len(re.findall(r"^--- FAIL: TestAudit", repro, re.M)),
        "recheck_checks_passing": len(re.findall(r"^--- PASS: TestRecheck", repro, re.M)),
        "recheck_checks_failing": len(re.findall(r"^--- FAIL: TestRecheck", repro, re.M)),
        "frontend_advisories": len(audit["advisories"]),
        "go_symbol_advisories_host": len(host["symbol_findings"]),
        "go_module_advisories_host": len(host["module_findings"]),
        "go_symbol_advisories_linux": len(linux["symbol_findings"]),
        "go_module_advisories_linux": len(linux["module_findings"]),
        "go_advisories_worktree": {k: len(t["symbol_findings"]) + len(t["module_findings"]) for k, t in worktree["targets"].items()},
        "go_worktree_scan_toolchain": worktree["go_version"],
    },
    "files": files,
}
(HERE / "manifest.json").write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n")
print(json.dumps(manifest["results"], indent=1))
