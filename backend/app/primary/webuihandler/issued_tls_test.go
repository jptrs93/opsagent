package webuihandler

import (
	"errors"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func TestDeploymentCreateRejectsIssuedTLSNamesOutsideSpace(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	admin := enforceCtx(1, false)
	create := func(name string, extraNames ...string) error {
		spec := remoteDeploymentSpec("busybox", virtualNetworking())
		spec.Workload.Value.Container.Runtime.IssuedTlsMount = apigen.Some(apigen.IssuedTLSMount{ContainerPath: "/opendeploy-tls", ExtraNames: extraNames})
		_, err := h.deploymentsCreate(admin, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Name: name, Spec: spec})
		return err
	}
	otherSpace := "api." + network.SpaceDNSName(int32(staging.ID)) + ".internal"
	var apiErr apigen.ApiErr
	if err := create("tls-other-space", otherSpace); !errors.As(err, &apiErr) || apiErr.Code != deployments.InvalidConfigErr.Code || !strings.Contains(err.Error(), "extraNames[0]") {
		t.Fatalf("create with %s: err = %v, want invalid config", otherSpace, err)
	}
	if err := create("tls-own-space", "issued-tls-extra.test", "api."+network.SpaceDNSName(int32(nodes.DefaultSpaceID))+".internal"); err != nil {
		t.Fatalf("create with own-space and external names: %v", err)
	}
}
