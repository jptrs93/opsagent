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
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	admin := enforceCtx(1, false)
	create := func(name string, extraNames ...string) error {
		spec := remoteDeploymentSpec("busybox", virtualNetworking())
		spec.Container1Spec.Runtime.IssuedTlsMount = &apigen.IssuedTLSMount{ContainerPath: "/opendeploy-tls", ExtraNames: extraNames}
		_, err := h.PostV1DeploymentsCreate(admin, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Name: name, Spec: spec})
		return err
	}
	otherSpace := "api." + network.SpaceDNSName(staging.ID) + ".internal"
	var apiErr apigen.ApiErr
	if err := create("tls-other-space", otherSpace); !errors.As(err, &apiErr) || apiErr.Code != deployments.InvalidConfigErr.Code || !strings.Contains(err.Error(), "extraNames[0]") {
		t.Fatalf("create with %s: err = %v, want invalid config", otherSpace, err)
	}
	if err := create("tls-own-space", "issued-tls-extra.test", "api."+network.SpaceDNSName(nodes.DefaultSpaceID)+".internal"); err != nil {
		t.Fatalf("create with own-space and external names: %v", err)
	}
}
