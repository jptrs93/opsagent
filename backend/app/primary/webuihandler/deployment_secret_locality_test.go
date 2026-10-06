package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func isSecretRefOutsideSpaceErr(err error) bool {
	var apiErr apigen.ApiErr
	return errors.As(err, &apiErr) && apiErr.InternalErr == deployments.SecretRefOutsideSpaceErr.InternalErr
}

func newSecretLocalityHandler(t *testing.T) (*Handler, *nodes.Node) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := ensureTestNode(store, "primary", "primary")
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	configService, err := systemconfig.InitializeService(store, *systemconfig.Default(systemconfig.DefaultInitial()))
	if err != nil {
		t.Fatalf("systemconfig.InitializeService: %v", err)
	}
	return &Handler{SystemConfig: configService, Store: store, Queries: store.Queries(), Secrets: secretsManager}, node
}

func secretEnvSpec(image string, ref apigen.ValueRef) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{
		"TOKEN": secretEnv(ref),
	}
	return spec
}

func TestDeploymentSecretRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSecret, err := h.Secrets.Create("global-token", []byte("g"), 0, nodes.DefaultSpaceID, 0)
	if err != nil {
		t.Fatalf("creating global secret: %v", err)
	}
	prodSecret, err := h.Secrets.Create("prod-token", []byte("p"), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating prod secret: %v", err)
	}

	create := func(name string, spaceID uint64, ref apigen.ValueRef) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec:       secretEnvSpec("nginx", ref),
		})
	}

	if _, err := create("own-space", prod.ID, prodSecret.Ref()); err != nil {
		t.Fatalf("own-space secret ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, globalSecret.Ref()); err != nil {
		t.Fatalf("global secret ref rejected: %v", err)
	}
	if _, err := create("global-deploy", nodes.DefaultSpaceID, prodSecret.Ref()); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("global deployment with prod secret err = %v, want %v", err, deployments.SecretRefOutsideSpaceErr)
	}
	if _, err := create("staging-deploy", staging.ID, prodSecret.Ref()); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("staging deployment with prod secret err = %v, want %v", err, deployments.SecretRefOutsideSpaceErr)
	}

	clean, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: staging.ID, Name: "clean",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err != nil {
		t.Fatalf("creating clean deployment: %v", err)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: clean.Deployment.ID,
		ExpectedSeq:  clean.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: secretEnvSpec("nginx", prodSecret.Ref())}},
	}); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("update adding prod secret err = %v, want %v", err, deployments.SecretRefOutsideSpaceErr)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: clean.Deployment.ID,
		ExpectedSeq:  clean.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: secretEnvSpec("nginx", globalSecret.Ref())}},
	}); err != nil {
		t.Fatalf("update adding global secret ref: %v", err)
	}
}

func TestIngressCertSecretRefScopedToSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"web.ingress.opendeploy.test"})
	if err != nil {
		t.Fatalf("generating certificate: %v", err)
	}
	certSecret, err := h.Secrets.Create("tls.web", append(certPEM, keyPEM...), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating cert secret: %v", err)
	}

	httpsSpec := func() apigen.DeploymentSpec {
		return remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []apigen.Ingress{{
				Hostname: "web.ingress.opendeploy.test",
				Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{
					ContainerPort:   8080,
					BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1,
					CertSource:      apigen.Some(apigen.CertSource{Value: apigen.CertSourceValueOneof{Secret: &apigen.SecretCertSource{Secret: apigen.SecretRef{SecretID: certSecret.Ref().ID, Version: certSecret.Ref().Version}}}}),
				}}},
			}},
		})
	}

	if _, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: nodes.DefaultSpaceID, Name: "web-global",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       httpsSpec(),
	}); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("global deployment with prod cert secret err = %v, want %v", err, deployments.SecretRefOutsideSpaceErr)
	}
	if _, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: prod.ID, Name: "web-prod",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       httpsSpec(),
	}); err != nil {
		t.Fatalf("own-space cert secret ref rejected: %v", err)
	}
}

func TestSecretMoveToGlobalAllowedWithOutsideRefs(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	secret, err := h.Secrets.Create("db-password", []byte("v"), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating secret: %v", err)
	}
	if _, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: prod.ID, Name: "db",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       secretEnvSpec("postgres", secret.Ref()),
	}); err != nil {
		t.Fatalf("creating referencing deployment: %v", err)
	}

	// The referencing deployment lives in prod: a move to the global space is
	// reference-safe, a move to any other space is not.
	if _, err := h.secretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: apigen.Some(nodes.DefaultSpaceID),
	}); err != nil {
		t.Fatalf("move to global with outside refs: %v", err)
	}
	if _, err := h.secretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: apigen.Some(staging.ID),
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("move out of global to staging err = %v, want %v", err, deployments.MoveReferencesOutsideSpaceErr)
	}
	if _, err := h.secretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: apigen.Some(prod.ID),
	}); err != nil {
		t.Fatalf("move back to the referencing space: %v", err)
	}
}
