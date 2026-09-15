#!/usr/bin/env python3
"""Closure check for OD-04 against the fix commit, via Go overlays; no source edits.

Reruns the audit's two OD-04 reproductions unchanged and adds one corrected
check through the deployment create handler, because the audit's deployments
test calls the syntax-only mount validator directly and that layer has no
space context; the space check runs in the create/update validation layer and
in the issuer. PASS on an audit test means the behaviour is still present at
the layer that test exercises; PASS on the corrected test means the fix holds.
Run: python3 docs/security-reviews/2026-09-13-evidence/closure_od04.py
"""
import importlib.util
import json
import pathlib
import subprocess
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[2]
spec = importlib.util.spec_from_file_location("audit_reproduce", HERE.parent / "2026-09-11-evidence" / "reproduce.py")
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)
OD04_AUDIT = {pkg: src for pkg, src in audit.TESTS.items() if pkg in ("app/primary/domain/deployments", "app/primary/domain/pki")}

CLOSURE_TESTS = {
    "app/primary/webuihandler": r'''
package webuihandler
import (
 "strconv"
 "strings"
 "testing"
 "github.com/jptrs93/opsagent/backend/apigen"
 "github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
)
func TestClosureOD04OtherSpaceNameRejectedAtCreate(t *testing.T) {
 h, staging := newEnforcementTestHandler(t)
 node := nodes.EnsurePrimaryNode(h.Store, "closure-primary", "closure-primary")
 spec := remoteDeploymentSpec("busybox", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})
 spec.Container1Spec.Runtime.IssuedTlsMount = &apigen.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: []string{"payments.space-" + strconv.Itoa(int(staging.ID)) + ".internal"}}
 _, err := h.PostV1DeploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Name: "closure-od04", Spec: spec})
 if err == nil || !strings.Contains(err.Error(), "extraNames[0]") { t.Fatalf("expected the other space's name to be rejected at create, got %v", err) }
 t.Log("FIXED: a cluster admin creating in the default space cannot claim another space's .internal name; the create path rejects it as invalid config.")
}
''',
}


def run(tests, name_prefix, pattern):
    with tempfile.TemporaryDirectory(prefix="opendeploy-closure-overlay-") as temp:
        temp = pathlib.Path(temp)
        replacements = {}
        for index, (package, source) in enumerate(tests.items()):
            testfile = temp / f"{name_prefix}_{index}_test.go"
            testfile.write_text(source)
            replacements[str(ROOT / "backend" / package / f"{name_prefix}_test.go")] = str(testfile)
        overlay = temp / "overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}))
        cmd = ["go", "test", "-overlay", str(overlay), "-count=1", "-run", pattern, "-v"]
        cmd.extend("./" + package for package in tests)
        result = subprocess.run(cmd, cwd=ROOT / "backend", text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return result.stdout


def main():
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    out = [f"# OD-04 closure check against {head}",
           "# Audit reproductions rerun unchanged (PASS = behaviour still present at the layer the test exercises)"]
    out.append(run(OD04_AUDIT, "security_audit_20260911", "TestAuditUnownedCertificateNameAccepted|TestAuditWorkloadCertificateVerifiesForUnownedName"))
    out.append("# Corrected check through the deployment create handler (PASS = the fix holds)")
    out.append(run(CLOSURE_TESTS, "security_closure_od04", "^TestClosureOD04"))
    text = "\n".join(out)
    (HERE / "od-04-closure-results.txt").write_text(text)
    print(text)


if __name__ == "__main__":
    main()
