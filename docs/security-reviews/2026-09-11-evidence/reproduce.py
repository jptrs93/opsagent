#!/usr/bin/env python3
"""Bounded audit checks against real packages via Go overlays; no source edits.

PASS means the audited behavior was reproduced, not that it is secure.
Only temporary databases/files and loopback HTTP servers are used. No workloads
are started, production credentials accessed, or external targets contacted.
Run: python3 docs/security-reviews/2026-09-11-evidence/reproduce.py
"""
import json
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[3]
TESTS = {
    "app/primary/webuihandler": r'''
package webuihandler
import (
 "errors"
 "testing"
 "github.com/go-webauthn/webauthn/webauthn"
 "github.com/jptrs93/opsagent/backend/apigen"
 "github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
 "github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)
func TestAuditAgentCanBindDeniedSecret(t *testing.T) {
 h, _ := newEnforcementTestHandler(t)
 node := nodes.EnsurePrimaryNode(h.Store, "audit-primary", "audit-primary")
 secret, err := h.Secrets.Create("audit-secret", []byte("synthetic-audit-value"), 1, nodes.DefaultSpaceID, 0)
 if err != nil { t.Fatal(err) }
 agent := enforceCtx(1, true)
 if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{ID: secret.ID}); !errors.Is(err, AccessDeniedErr) { t.Fatalf("direct reveal should be denied: %v", err) }
 spec := secretEnvSpec("busybox", secret.ID)
 dep, err := h.PostV1DeploymentsCreate(agent, &apigen.DeploymentCreateRequest{SpaceID:nodes.DefaultSpaceID, NodeID:node.ID, Name:"audit-secret-consumer", Spec:spec})
 if err != nil { t.Fatal(err) }
 if dep.Value.Spec.Container().Runtime.EnvVars["TOKEN"].SecretVersionID == nil { t.Fatal("missing secret binding") }
 t.Log("REPRODUCED: delegated secret reveal denied; same agent's workload with secret environment binding accepted. Workload was not run.")
}
func TestAuditSpaceOperatorCanMountProtectedParent(t *testing.T) {
 h, _ := newEnforcementTestHandler(t)
 node := nodes.EnsurePrimaryNode(h.Store, "audit-primary", "audit-primary")
 spec := remoteDeploymentSpec("busybox", hostNetworking())
 spec.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{HostPath:"/var/lib", ContainerPath:"/host-state", Permission:apigen.FilePermission_READ_WRITE}}
 if _, err := h.PostV1DeploymentsCreate(enforceCtx(2,false), &apigen.DeploymentCreateRequest{SpaceID:nodes.DefaultSpaceID, NodeID:node.ID, Name:"audit-parent-mount", Spec:spec}); err != nil { t.Fatal(err) }
 t.Log("REPRODUCED: built-in space_admin can submit a read-write /var/lib host mount. No mount executed.")
}
func TestAuditPasskeyUpdatesAppendDuplicateIDs(t *testing.T) {
 h, _ := newEnforcementTestHandler(t)
 snapshot := h.SystemConfig.Snapshot(); h.Config = &snapshot.Settings
 h.Config.HttpWeb.Enabled.Value = true
 user, err := users.ByID(h.Store.Queries(),1); if err != nil { t.Fatal(err) }
 user.WebAuthNID = []byte("audit-user"); users.Write(h.Store,user)
 if err := h.initPasskeyService(); err != nil { t.Fatal(err) }
 credential := &webauthn.Credential{ID:[]byte("audit-credential")}
 if err := h.PasskeyService.SaveCredential(user.WebAuthNID,credential); err != nil { t.Fatal(err) }
 credential.Authenticator.SignCount = 7
 if err := h.PasskeyService.SaveCredential(user.WebAuthNID,credential); err != nil { t.Fatal(err) }
 saved, err := users.ByID(h.Store.Queries(),1); if err != nil { t.Fatal(err) }
 creds := saved.WebAuthnCredentials()
 if len(creds)!=2 || creds[0].Authenticator.SignCount!=0 || creds[1].Authenticator.SignCount!=7 { t.Fatalf("unexpected saved credential state: %#v",creds) }
 t.Log("REPRODUCED: saving one credential twice creates two rows in the user record, retaining the first stale counter. Callback tested; no authenticator used.")
}
''',
    "app/primary/domain/deployments": r'''
package deployments
import (
 "os"
 "path/filepath"
 "testing"
 "github.com/jptrs93/opsagent/backend/apigen"
)
func TestAuditMountParentAndSymlinkValidation(t *testing.T) {
 for _, host := range []string{"/var", "/var/lib"} {
  err := validateCustomHostMounts([]*apigen.CustomHostMount{{HostPath:host,ContainerPath:"/audit",Permission:apigen.FilePermission_READ_WRITE}})
  if err != nil { t.Fatalf("%s: %v",host,err) }
 }
 if !containerHostMountDenied("/var/lib/opendeploy") { t.Fatal("negative control: direct protected path should be denied") }
 alias := filepath.Join(t.TempDir(),"alias")
 if err := os.Symlink("/etc",alias); err != nil { t.Fatal(err) }
 if err := validateCustomHostMounts([]*apigen.CustomHostMount{{HostPath:alias,ContainerPath:"/audit",Permission:apigen.FilePermission_READ_ONLY}}); err != nil { t.Fatal(err) }
 t.Log("REPRODUCED: protected child denied but /var, /var/lib and a symlink to /etc accepted. No protected files read or mounted.")
}
func TestAuditUnownedCertificateNameAccepted(t *testing.T) {
 mount := &apigen.IssuedTLSMount{ContainerPath:"/tls",ExtraNames:[]string{"payments.space-999.internal"}}
 if err := validateIssuedTLSMount(mount); err != nil { t.Fatal(err) }
 t.Log("REPRODUCED: a different space's DNS name passes workload certificate validation.")
}
''',
    "app/primary/domain/pki": r'''
package pki
import (
 "crypto/x509"
 "testing"
 "github.com/jptrs93/opsagent/backend/apigen"
 "github.com/jptrs93/opsagent/backend/util/certu"
)
func TestAuditWorkloadCertificateVerifiesForUnownedName(t *testing.T) {
 issuer := &Issuer{Secrets:newTestSecrets(t)}
 cfg := &apigen.DeploymentEvent{DeploymentID:42,Value:apigen.Deployment{Name:"audit",SpaceID:2,Spec:apigen.DeploymentSpec{Container1Spec:&apigen.ContainerSpec{Runtime:apigen.ContainerRuntime{IssuedTlsMount:&apigen.IssuedTLSMount{ContainerPath:"/tls",ExtraNames:[]string{"payments.space-999.internal"}}}}}}}
 result, err := issuer.Issue(cfg); if err != nil { t.Fatal(err) }
 _, leaf, err := certu.ParseCertificate(result.CertPem,"audit leaf"); if err != nil { t.Fatal(err) }
 roots := x509.NewCertPool(); roots.AppendCertsFromPEM(result.CaCertPem)
 if _, err := leaf.Verify(x509.VerifyOptions{Roots:roots,DNSName:"payments.space-999.internal",KeyUsages:[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil { t.Fatal(err) }
 t.Log("REPRODUCED: certificate for deployment in space 2 verifies as payments.space-999.internal under the shared workload CA.")
}
''',
    "lib/engine/versionprovider": r'''
package versionprovider
import (
 "context"
 "fmt"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/jptrs93/opsagent/backend/lib/engine/imageref"
)
func TestAuditRegistryRealmCallsLoopback(t *testing.T) {
 calls:=0
 target:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){calls++;fmt.Fprint(w,`{"token":"synthetic-token"}`)}));defer target.Close()
 _,err:=registryBearerToken(context.Background(),target.Client(),`Bearer realm="`+target.URL+`/audit"`)
 if err!=nil || calls!=1 { t.Fatalf("calls=%d err=%v",calls,err) }
 t.Log("REPRODUCED: registry bearer realm triggers a GET to a loopback HTTP URL.")
}
func TestAuditRegistryPaginationForwardsBearerToAnotherOrigin(t *testing.T) {
 gotAuth:=""
 sink:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){gotAuth=r.Header.Get("Authorization");fmt.Fprint(w,`{"tags":["audit"]}`)}));defer sink.Close()
 var registry *httptest.Server
 registry=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/token" { fmt.Fprint(w,`{"token":"synthetic-registry-token"}`);return }
  if r.Header.Get("Authorization")=="" { w.Header().Set("WWW-Authenticate",`Bearer realm="`+registry.URL+`/token"`);w.WriteHeader(401);return }
  w.Header().Set("Link","<"+sink.URL+`/audit>; rel="next"`);fmt.Fprint(w,`{"tags":[]}`)
 }));defer registry.Close()
 _,err:=listImageTags(context.Background(),registry.Client(),imageref.Repository{Registry:strings.TrimPrefix(registry.URL,"https://"),Name:"audit"})
 if err!=nil {t.Fatal(err)}
 if gotAuth!="Bearer synthetic-registry-token" {t.Fatalf("unexpected header: %q",gotAuth)}
 t.Log("REPRODUCED: non-GHCR registry pagination sends bearer token to another origin over HTTP. All traffic stayed on loopback.")
}
''',
}

def main():
    with tempfile.TemporaryDirectory(prefix="opendeploy-audit-overlay-") as temp:
        temp = pathlib.Path(temp)
        replacements = {}
        for index, (package, source) in enumerate(TESTS.items()):
            testfile = temp / f"audit_{index}_test.go"
            testfile.write_text(source)
            replacements[str(ROOT / "backend" / package / "security_audit_20260911_test.go")] = str(testfile)
        overlay = temp / "overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}))
        cmd = ["go", "test", "-overlay", str(overlay), "-count=1", "-run", "^TestAudit", "-v"]
        cmd.extend("./" + package for package in TESTS)
        result = subprocess.run(cmd, cwd=ROOT / "backend", text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        log = pathlib.Path(__file__).with_name("reproduction-results.txt")
        log.write_text(result.stdout)
        print(result.stdout)
        raise SystemExit(result.returncode)

if __name__ == "__main__":
    main()
