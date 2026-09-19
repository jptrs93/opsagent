#!/usr/bin/env python3
"""Write manifest.json: commit, commands, headline results and sha256 of every evidence file."""
import hashlib, json, pathlib, platform, re, subprocess
HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[2]
go = subprocess.check_output(["go", "version"], text=True).split()[2]
repro = (HERE / "reproduction-results.txt").read_text()
gotests = (HERE / "go-tests.txt").read_text()
fe = (HERE / "frontend-tests.txt").read_text()
host = json.loads((HERE / "govulncheck-host-summary.json").read_text())
linux = json.loads((HERE / "govulncheck-linux-summary.json").read_text())
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
    "release": findings.get("release"),
    "platform": platform.platform(),
    "go": go,
    "commands": {
        "go_tests": "go test -count=1 ./... (backend/)",
        "frontend_tests": "pnpm test (frontend/)",
        "frontend_audit": "pnpm audit --json; pnpm audit --prod --json (frontend/)",
        "host_scan": "go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -json ./... (backend/)",
        "linux_scan": "GOOS=linux GOARCH=amd64 govulncheck -json ./... (v1.8.0 installed to a scratch GOBIN)",
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
        "review_reproductions_passing": len(re.findall(r"^--- PASS: TestReview", repro, re.M)),
        "frontend_advisories": len(audit["advisories"]),
        "go_symbol_advisories_host": len(host["symbol_findings"]),
        "go_module_advisories_host": len(host["module_findings"]),
        "go_symbol_advisories_linux": len(linux["symbol_findings"]),
        "go_module_advisories_linux": len(linux["module_findings"]),
    },
    "files": files,
}
(HERE / "manifest.json").write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n")
print(json.dumps(manifest["results"], indent=1))
