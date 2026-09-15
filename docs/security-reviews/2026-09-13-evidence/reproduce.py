#!/usr/bin/env python3
"""Rerun the 11 September audit reproductions against the current tree, plus two
corrected checks, via Go overlays; no source edits.

The audit's own tests are imported unchanged from the 11 September evidence
directory. Two of them use host networking, which the use_host_network gate
added on 12 September now denies before the audited behaviour is reached, so
the corrected checks repeat them with virtual networking. PASS means the
described behaviour was reproduced, not that it is secure.
Run: python3 docs/security-reviews/2026-09-13-evidence/reproduce.py
"""
import json
import pathlib
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE.parent / "2026-09-11-evidence"))
from reproduce import TESTS as AUDIT_TESTS  # noqa: E402

REVIEW_TESTS = {
    "app/primary/webuihandler": r'''
package webuihandler
import (
 "errors"
 "testing"
 "github.com/jptrs93/opsagent/backend/apigen"
 "github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
)
func reviewVirtualNetworking() apigen.NetworkingConfig { return apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL} }
func TestReviewAgentCanBindDeniedSecretWithVirtualNetworking(t *testing.T) {
 h, _ := newEnforcementTestHandler(t)
 node := nodes.EnsurePrimaryNode(h.Store, "review-primary", "review-primary")
 secret, err := h.Secrets.Create("review-secret", []byte("synthetic-review-value"), 1, nodes.DefaultSpaceID, 0)
 if err != nil { t.Fatal(err) }
 agent := enforceCtx(1, true)
 if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{ID: secret.ID}); !errors.Is(err, AccessDeniedErr) { t.Fatalf("direct reveal should be denied: %v", err) }
 spec := remoteDeploymentSpec("busybox", reviewVirtualNetworking())
 id := secret.ID
 spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"TOKEN": {SecretVersionID: &id}}
 dep, err := h.PostV1DeploymentsCreate(agent, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Name: "review-secret-consumer", Spec: spec})
 if err != nil { t.Fatalf("create with denied secret binding: %v", err) }
 if dep.Value.Spec.Container().Runtime.EnvVars["TOKEN"].SecretVersionID == nil { t.Fatal("missing secret binding") }
 t.Log("REPRODUCED: delegated secret reveal denied; same agent's virtual-network workload with the secret bound into env accepted. Workload was not run.")
}
func TestReviewSpaceAdminHostMountDeniedWithVirtualNetworking(t *testing.T) {
 h, _ := newEnforcementTestHandler(t)
 node := nodes.EnsurePrimaryNode(h.Store, "review-primary", "review-primary")
 spec := remoteDeploymentSpec("busybox", reviewVirtualNetworking())
 spec.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{HostPath: "/srv/data", ContainerPath: "/host-state", Permission: apigen.FilePermission_READ_WRITE}}
 _, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Name: "review-host-mount", Spec: spec})
 if !errors.Is(err, AccessDeniedErr) { t.Fatalf("expected access denied for a space admin host mount, got %v", err) }
 t.Log("FIXED: built-in space_admin can no longer submit a custom host mount even for an unprotected path; the gate is the use_host_mounts verb, not the denylist.")
}
''',
}


def run(tests, name_prefix, pattern):
    with tempfile.TemporaryDirectory(prefix="opendeploy-rereview-overlay-") as temp:
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
    head = subprocess.check_output(["git", "rev-parse", "--short", "HEAD"], cwd=ROOT, text=True).strip()
    out = [f"# 11 September audit reproductions rerun unchanged against {head} (PASS = behaviour still present)"]
    audit = run(AUDIT_TESTS, "security_audit_20260911", "^TestAudit")
    out.append(audit)
    out.append("# Corrected reproductions (virtual networking, so the host-network gate does not mask the result)")
    review = run(REVIEW_TESTS, "security_rereview_20260913", "^TestReview")
    out.append(review)
    text = "\n".join(out)
    (HERE / "reproduction-results.txt").write_text(text)
    print(text)


if __name__ == "__main__":
    main()
