#!/usr/bin/env python3
"""Bounded checks against the current tree via Go overlays; no source edits.

22 November 2026 re-check, updated on 23 November. The data-model rewrite between v0.0.609 and v0.0.617
changed the API the 11 September overlay tests were written against, so all but
the two registry tests no longer compile. Those two are imported unchanged from
the 11 September evidence directory; the rest are ported here to the current
API and joined by closure checks for the findings that closed (OD-12, RR-13,
RR-27, and the write-time half of RR-05), regression checks for earlier closures (OD-02 gate, OD-04) and a
reproduction of a finding raised on 22 November (RR-29).

The 22 November reproduction of RR-27 is kept unchanged. RR-27 was fixed in
commit ca3ff94 on 23 November, so that test is now expected to FAIL (the
link is refused), and a closure check beside it must PASS.

Each test logs one of:
  REPRODUCED  the described behaviour is still present (PASS = still open;
              FAIL = it no longer reproduces)
  CLOSED      the closure criterion is met (PASS = closed)
  HOLDS       an earlier closure still holds (PASS = still closed)

Only temporary databases and loopback HTTP servers are used. No workload is
started and nothing outside the test process is contacted.
Run: python3 docs/security-reviews/2026-09-13-evidence/reproduce.py
"""
import importlib.util
import json
import pathlib
import subprocess
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[2]
_spec = importlib.util.spec_from_file_location("audit_reproduce", HERE.parent / "2026-09-11-evidence" / "reproduce.py")
_audit = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_audit)
AUDIT_TESTS = {pkg: src for pkg, src in _audit.TESTS.items() if pkg == "lib/engine/versionprovider"}

RECHECK_TESTS = {
    "app/primary/webuihandler": r'''
package webuihandler

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestRecheckAgentCanBindDeniedSecret(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "recheck-primary", "recheck-primary")
	secret, err := h.Secrets.Create("recheck-secret", []byte("synthetic-recheck-value"), 1, nodes.DefaultSpaceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	agent := enforceCtx(1, true)
	ref := secret.Ref()
	if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{SecretID: ref.ID, Version: ref.Version}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("direct reveal should be denied: %v", err)
	}
	spec := remoteDeploymentSpec("busybox", virtualNetworking())
	spec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{"TOKEN": secretEnv(ref)}
	if _, err := h.PostV1DeploymentsCreate(agent, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "recheck-secret-consumer", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec}); err != nil {
		t.Fatalf("create with denied secret binding: %v", err)
	}
	t.Log("REPRODUCED: delegated secret reveal denied; the same agent's virtual-network workload with the secret bound into env is accepted. Workload was not run. Accepted by design (AP-1).")
}

func TestRecheckSpaceAdminHostMountDenied(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "recheck-primary", "recheck-primary")
	spec := remoteDeploymentSpec("busybox", virtualNetworking())
	spec.Workload.Value.Container.Runtime.Mounts = []apigen.HostMount{{HostPath: "/srv/data", ContainerPath: "/host-state", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE}}
	_, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "recheck-host-mount", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec})
	if !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("expected access denied for a space admin host mount, got %v", err)
	}
	t.Log("HOLDS: built-in space_admin cannot submit a custom host mount even for an unprotected path; the gate is the use_host_mounts verb.")
}

func TestRecheckOtherSpaceCertificateNameRejectedAtCreate(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "recheck-primary", "recheck-primary")
	spec := remoteDeploymentSpec("busybox", virtualNetworking())
	spec.Workload.Value.Container.Runtime.IssuedTlsMount = apigen.Some(apigen.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: []string{"payments.space-" + strconv.Itoa(int(staging.ID)) + ".internal"}})
	_, err := h.PostV1DeploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "recheck-od04", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec})
	if err == nil || !strings.Contains(err.Error(), "extraNames[0]") {
		t.Fatalf("expected the other space's name to be rejected at create, got %v", err)
	}
	t.Log("HOLDS: a cluster admin creating in the global space cannot claim another space's .internal name (OD-04 stays closed).")
}

func TestRecheckPasskeySaveReplacesByCredentialID(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.Config = func() *apigen.ClusterSettings { s := h.SystemConfig.Snapshot().Settings; return &s }
	user, err := users.ByID(h.Store.Queries(), 1)
	if err != nil {
		t.Fatal(err)
	}
	user.Authentication.WebAuthnID = []byte("recheck-user")
	users.Write(h.Store, user)
	if err := h.initPasskeyService(); err != nil {
		t.Fatal(err)
	}
	credential := &webauthn.Credential{ID: []byte("recheck-credential")}
	if err := h.PasskeyService.SaveCredential(user.Authentication.WebAuthnID, credential); err != nil {
		t.Fatal(err)
	}
	credential.Authenticator.SignCount = 7
	if err := h.PasskeyService.SaveCredential(user.Authentication.WebAuthnID, credential); err != nil {
		t.Fatal(err)
	}
	saved, err := users.ByID(h.Store.Queries(), 1)
	if err != nil {
		t.Fatal(err)
	}
	creds := saved.Authentication.Credentials
	if len(creds) != 1 || !strings.Contains(string(creds[0].Data), `"signCount":7`) {
		t.Fatalf("unexpected saved credential state: %d rows, %s", len(creds), creds[0].Data)
	}
	t.Log("CLOSED: saving one credential twice through the service callback leaves one row carrying the latest sign counter (OD-12).")
}

func spaceInUseMessage(err error) string {
	var apiErr apigen.ApiErr
	if !errors.As(err, &apiErr) || apiErr.InternalErr != "space_in_use" {
		return ""
	}
	return apiErr.DisplayErr
}

func TestRecheckSpaceIDNotReusedAndDeleteRefusedWhileReferenced(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	held, err := nodes.CreateSpace(h.Store, "held", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secrets.Create("held-secret", []byte("synthetic"), 1, held.ID, 0); err != nil {
		t.Fatal(err)
	}
	if msg := spaceInUseMessage(h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: held.ID})); !strings.Contains(msg, "1 secret") {
		t.Fatalf("delete of a space holding a secret should be refused, got %q", msg)
	}
	granted, err := nodes.CreateSpace(h.Store, "granted", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Authz.CreateGrant(templateGrant(2, authz.SpaceAdminTemplateID, spaceBinding(1, granted.ID)), 0); err != nil {
		t.Fatal(err)
	}
	if msg := spaceInUseMessage(h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: granted.ID})); !strings.Contains(msg, "1 access grant") {
		t.Fatalf("delete of a space named by a grant should be refused, got %q", msg)
	}
	last, err := nodes.CreateSpace(h.Store, "last", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: last.ID}); err != nil {
		t.Fatalf("delete of an empty space: %v", err)
	}
	next, err := nodes.CreateSpace(h.Store, "next", 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID <= last.ID {
		t.Fatalf("space id reused: deleted %d, new %d", last.ID, next.ID)
	}
	t.Logf("CLOSED: deleting the highest space (%d) and creating another yields %d; spaces holding a secret or named by a grant cannot be deleted (RR-13).", last.ID, next.ID)
}

func TestRecheckUserUpdateMintsSignInLinkForAnAdmin(t *testing.T) {
	h, admin := newAuthTestHandler(t)
	setupAuth(t, h, admin)
	if _, err := h.Authz.CreateGrant(clusterAdminGrant(admin.ID), 0); err != nil {
		t.Fatal(err)
	}
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("helpdesk")})
	if _, err := h.Authz.CreateGrant(ruleGrant(2, allowRule(apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_UPDATE),
		Spaces:      exactSpaces(0),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER),
		EntityRefs:  allEntityRefs(),
	})), 0); err != nil {
		t.Fatal(err)
	}
	helpdesk := h.operatorCtx(t, &apigen.User{ID: 2})
	if err := h.requireAccess(helpdesk, vCreate, eAccess, 0, 0); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("the helpdesk user must not hold access:create, got %v", err)
	}
	link, err := h.PostV1UsersLink(helpdesk, &apigen.UserLinkRequest{UserID: admin.ID})
	if err != nil {
		t.Fatalf("link for the admin was refused: %v", err)
	}
	redeemed, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: link.Token})
	if err != nil {
		t.Fatal(err)
	}
	asAdmin, err := h.verifyToken(redeemed.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	if asAdmin.User == nil || asAdmin.User.ID != admin.ID {
		t.Fatalf("redeemed session is not the admin's: %+v", asAdmin.User)
	}
	if err := h.requireAccess(asAdmin, vCreate, eAccess, 0, 0); err != nil {
		t.Fatalf("redeemed session lacks access:create: %v", err)
	}
	t.Log("REPRODUCED: a user holding only user:update in space 0 issued a sign-in link for a cluster admin, redeemed it, and holds a full session with access:create (RR-27).")
}

func TestRecheckSignInLinkRefusedWithoutAccessCreate(t *testing.T) {
	h, admin := newAuthTestHandler(t)
	setupAuth(t, h, admin)
	if _, err := h.Authz.CreateGrant(clusterAdminGrant(admin.ID), 0); err != nil {
		t.Fatal(err)
	}
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("helpdesk")})
	if _, err := h.Authz.CreateGrant(ruleGrant(2, allowRule(apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_CREATE, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, apigen.AuthzVerb_AUTHZ_VERB_DELETE, apigen.AuthzVerb_AUTHZ_VERB_VIEW),
		Spaces:      exactSpaces(0),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER),
		EntityRefs:  allEntityRefs(),
	})), 0); err != nil {
		t.Fatal(err)
	}
	helpdesk := h.operatorCtx(t, &apigen.User{ID: 2})
	if err := h.requireAccess(helpdesk, vUpdate, eUser, 0, 0); err != nil {
		t.Fatalf("control: the helpdesk user must hold user:update, got %v", err)
	}
	if err := h.requireAccess(helpdesk, vCreate, eAccess, 0, 0); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("control: the helpdesk user must not hold access:create, got %v", err)
	}
	if _, err := h.PostV1UsersLink(helpdesk, &apigen.UserLinkRequest{UserID: admin.ID}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("link for the admin without access:create: err = %v, want AccessDeniedErr", err)
	}
	link, err := h.PostV1UsersLink(h.operatorCtx(t, admin), &apigen.UserLinkRequest{UserID: 2})
	if err != nil {
		t.Fatalf("control: a cluster admin must still be able to issue a link: %v", err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: link.Token}); err != nil {
		t.Fatalf("control: the admin-issued link must redeem: %v", err)
	}
	t.Log("CLOSED: a user holding every verb on the user type and no access:create is refused a sign-in link for a cluster admin; a cluster admin can still issue and the link redeems (RR-27).")
}

func TestRecheckPortForwardClaimsRejectedAtWriteTime(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "recheck-worker", "recheck-worker")
	forward := func(protocol apigen.PortForwardProtocol, hostPort uint32) apigen.DeploymentSpec {
		spec := remoteDeploymentSpec("busybox", virtualNetworking())
		spec.Networking.PortForwarding = []apigen.PortForward{{Protocol: protocol, HostPort: hostPort, ContainerPort: 8080}}
		return spec
	}
	create := func(ctx apigen.Context, spaceID uint64, name string, spec apigen.DeploymentSpec) error {
		_, err := h.PostV1DeploymentsCreate(ctx, &apigen.DeploymentCreateRequest{SpaceID: spaceID, Name: name, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec})
		return err
	}
	tcp, udp := apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	admin, spaceAdmin := enforceCtx(1, false), enforceCtx(2, false)
	if err := create(admin, staging.ID, "recheck-holder", forward(tcp, 18080)); err != nil {
		t.Fatal(err)
	}
	if err := create(admin, staging.ID, "recheck-holder-udp", forward(udp, 15353)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ctx  apigen.Context
		spec apigen.DeploymentSpec
		want string
	}{
		{"recheck-tcp-taken", spaceAdmin, forward(tcp, 18080), "TCP host port 18080 is already forwarded by another deployment"},
		{"recheck-udp-taken", spaceAdmin, forward(udp, 15353), "UDP host port 15353 is already forwarded by another deployment"},
		{"recheck-wireguard", admin, forward(udp, 51833), "reserved by the WireGuard cluster transport"},
	} {
		if err := create(tc.ctx, nodes.DefaultSpaceID, tc.name, tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want a rejection containing %q", tc.name, err, tc.want)
		}
	}
	err := create(spaceAdmin, nodes.DefaultSpaceID, "recheck-ssh", forward(tcp, 22))
	var apiErr apigen.ApiErr
	if !errors.Is(err, AccessDeniedErr) || !errors.As(err, &apiErr) || !strings.Contains(apiErr.DisplayErr, "use_privileged_ports") {
		t.Fatalf("a space admin forwarding port 22: err = %v, want access denied naming use_privileged_ports", err)
	}
	if err := create(spaceAdmin, nodes.DefaultSpaceID, "recheck-free", forward(tcp, 18081)); err != nil {
		t.Fatalf("control: a free high port must still be accepted: %v", err)
	}
	t.Log("CLOSED: a second forward on a taken TCP or UDP port and a forward on the WireGuard port are rejected at write time, and a space admin is denied a forward on port 22 (RR-05).")
}

func TestRecheckEpochStatusClockBreaksTheOpeningSnapshot(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	spec := remoteDeploymentSpec("busybox", virtualNetworking())
	dep := createTestDeployment(h.Store, "recheck-worker", nodes.DefaultSpaceID, "recheck-status", &spec)
	inst := statetest.CreateScheduledInstance(h.Store, dep.Deployment.ID, dep.Meta.SpecVersion, dep.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	before, err := h.PostV1GlobalEvents(admin, &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.EncodeChecked(); err != nil {
		t.Fatalf("negative control: the opening snapshot must encode before the report: %v", err)
	}

	// What a worker puts on the wire: a status whose updated_at submessage is
	// present and empty (seconds 0, nanos 0).
	wire := (&apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, UpdatedAt: apigen.Some(time.Unix(0, 0))}).Encode()
	report, err := apigen.DecodeScheduledInstanceStatus(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("the report did not pass request validation: %v", err)
	}
	scheduledinstances.WriteReplicatedStatus(h.Store, report)

	after, err := h.PostV1GlobalEvents(admin, &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatalf("opening snapshot: %v", err)
	}
	if _, err := after.EncodeChecked(); err == nil {
		t.Fatal("the opening snapshot still encodes after the epoch-clock status")
	} else {
		t.Logf("REPRODUCED: one status report with an epoch updated_at for the node's own instance passes validation and commits; the admin's opening snapshot then fails to encode: %v (RR-29).", err)
	}
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

func TestRecheckMountParentRejectedSymlinkAccepted(t *testing.T) {
	for _, host := range []string{"/", "/var", "/var/lib", "/var/lib/opendeploy"} {
		if err := validateCustomHostMounts([]apigen.HostMount{{HostPath: host, ContainerPath: "/recheck", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE}}); err == nil {
			t.Fatalf("%s: expected the protected root or its parent to be rejected", host)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink("/var/lib/opendeploy", alias); err != nil {
		t.Fatal(err)
	}
	if err := validateCustomHostMounts([]apigen.HostMount{{HostPath: alias, ContainerPath: "/recheck", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY}}); err != nil {
		t.Fatalf("symlink into a protected root was rejected: %v", err)
	}
	t.Log("REPRODUCED: /, /var, /var/lib and /var/lib/opendeploy are rejected, but a symlink that resolves to /var/lib/opendeploy passes the lexical validator (OD-02 residual). Nothing was mounted.")
}
''',
}


def run(tests, name_prefix, pattern):
    with tempfile.TemporaryDirectory(prefix="opendeploy-recheck-overlay-") as temp:
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
    dirty = subprocess.check_output(["git", "status", "--porcelain", "--", "backend"], cwd=ROOT, text=True).strip()
    if dirty:
        head += " plus uncommitted changes under backend/"
    out = [f"# 11 September registry reproductions rerun unchanged against {head} (PASS = behaviour still present)"]
    out.append(run(AUDIT_TESTS, "security_audit_20260911", "^TestAudit"))
    out.append(f"# Re-check tests against {head}, ported to the current API (see each log line: REPRODUCED, CLOSED or HOLDS)")
    out.append(run(RECHECK_TESTS, "security_recheck_20261122", "^TestRecheck"))
    text = "\n".join(out)
    (HERE / "reproduction-results.txt").write_text(text)
    print(text)


if __name__ == "__main__":
    main()
