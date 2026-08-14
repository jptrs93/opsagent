package webuihandler

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func isSecretRefOutsideSpaceErr(err error) bool {
	var apiErr apigen.ApiErr
	return errors.As(err, &apiErr) && apiErr.InternalErr == SecretRefOutsideSpaceErr.InternalErr
}

func newSecretLocalityHandler(t *testing.T) (*Handler, *state.Node) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	configService, err := config.InitializeService(store, apigen.PrimaryConfig{})
	if err != nil {
		t.Fatalf("config.InitializeService: %v", err)
	}
	return &Handler{ConfigService: configService, Store: store, Secrets: secretsManager}, node
}

func secretEnvSpec(image string, secretVersionID int32) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	id := secretVersionID
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"TOKEN": {SecretVersionID: &id},
	}
	return spec
}

func TestDeploymentSecretRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSecret, err := h.Secrets.Create("global-token", []byte("g"), 0, state.DefaultSpaceID, 0)
	if err != nil {
		t.Fatalf("creating global secret: %v", err)
	}
	prodSecret, err := h.Secrets.Create("prod-token", []byte("p"), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating prod secret: %v", err)
	}

	create := func(name string, spaceID, secretVersionID int32) (*apigen.DeploymentConfig, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			Identity: apigen.DeploymentIdentity{SpaceID: spaceID, Name: name},
			NodeID:   node.ID,
			Spec:     secretEnvSpec("nginx", secretVersionID),
		})
	}

	if _, err := create("own-space", prod.ID, prodSecret.ID); err != nil {
		t.Fatalf("own-space secret ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, globalSecret.ID); err != nil {
		t.Fatalf("global secret ref rejected: %v", err)
	}
	if _, err := create("global-deploy", state.DefaultSpaceID, prodSecret.ID); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("global deployment with prod secret err = %v, want %v", err, SecretRefOutsideSpaceErr)
	}
	if _, err := create("staging-deploy", staging.ID, prodSecret.ID); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("staging deployment with prod secret err = %v, want %v", err, SecretRefOutsideSpaceErr)
	}

	clean, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: staging.ID, Name: "clean"},
		NodeID:   node.ID,
		Spec:     remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err != nil {
		t.Fatalf("creating clean deployment: %v", err)
	}
	if _, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: clean.ID,
		Version:      clean.Version + 1,
		Spec:         secretEnvSpec("nginx", prodSecret.ID),
	}); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("update adding prod secret err = %v, want %v", err, SecretRefOutsideSpaceErr)
	}
	if _, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: clean.ID,
		Version:      clean.Version + 1,
		Spec:         secretEnvSpec("nginx", globalSecret.ID),
	}); err != nil {
		t.Fatalf("update adding global secret ref: %v", err)
	}
}

func TestDeploymentSpaceMoveRechecksSecretRefs(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSecret, err := h.Secrets.Create("global-token", []byte("g"), 0, state.DefaultSpaceID, 0)
	if err != nil {
		t.Fatalf("creating global secret: %v", err)
	}
	prodSecret, err := h.Secrets.Create("prod-token", []byte("p"), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating prod secret: %v", err)
	}

	pinned, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: prod.ID, Name: "pinned"},
		NodeID:   node.ID,
		Spec:     secretEnvSpec("nginx", prodSecret.ID),
	})
	if err != nil {
		t.Fatalf("creating pinned deployment: %v", err)
	}
	if _, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: pinned.ID,
		Version:      pinned.Version + 1,
		SpaceID:      &staging.ID,
	}); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("space move with own-space pin err = %v, want %v", err, SecretRefOutsideSpaceErr)
	}

	portable, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: prod.ID, Name: "portable"},
		NodeID:   node.ID,
		Spec:     secretEnvSpec("nginx", globalSecret.ID),
	})
	if err != nil {
		t.Fatalf("creating portable deployment: %v", err)
	}
	if _, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: portable.ID,
		Version:      portable.Version + 1,
		SpaceID:      &staging.ID,
	}); err != nil {
		t.Fatalf("space move with only global pins: %v", err)
	}
}

func TestIngressCertSecretRefScopedToSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
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
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_HTTPS,
				Hostname: "web.ingress.opendeploy.test",
				HttpsConfig: &apigen.HttpsConfig{
					ContainerPort: 8080,
					CertSource:    &apigen.CertSource{Secret: &apigen.SecretCertSource{SecretVersionID: certSecret.ID}},
				},
			}},
		})
	}

	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: state.DefaultSpaceID, Name: "web-global"},
		NodeID:   node.ID,
		Spec:     httpsSpec(),
	}); !isSecretRefOutsideSpaceErr(err) {
		t.Fatalf("global deployment with prod cert secret err = %v, want %v", err, SecretRefOutsideSpaceErr)
	}
	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: prod.ID, Name: "web-prod"},
		NodeID:   node.ID,
		Spec:     httpsSpec(),
	}); err != nil {
		t.Fatalf("own-space cert secret ref rejected: %v", err)
	}
}

func TestSecretMoveToGlobalAllowedWithOutsideRefs(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	secret, err := h.Secrets.Create("db-password", []byte("v"), 0, prod.ID, 0)
	if err != nil {
		t.Fatalf("creating secret: %v", err)
	}
	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: prod.ID, Name: "db"},
		NodeID:   node.ID,
		Spec:     secretEnvSpec("postgres", secret.ID),
	}); err != nil {
		t.Fatalf("creating referencing deployment: %v", err)
	}

	// The referencing deployment lives in prod: a move to the global space is
	// reference-safe, a move to any other space is not.
	if _, err := h.PostV1SecretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: state.DefaultSpaceID,
	}); err != nil {
		t.Fatalf("move to global with outside refs: %v", err)
	}
	if _, err := h.PostV1SecretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: staging.ID,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("move out of global to staging err = %v, want %v", err, MoveReferencesOutsideSpaceErr)
	}
	if _, err := h.PostV1SecretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: prod.ID,
	}); err != nil {
		t.Fatalf("move back to the referencing space: %v", err)
	}
}
