package webuihandler

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestConcurrentCreatesRejectDuplicateIdentity(t *testing.T) {
	h, _, _ := newV2DeploymentHandler(t)
	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
				SpaceID: 1, Name: "raced",
				NodeID: 1,
				Spec:   remoteDeploymentSpec("nginx", hostNetworking()),
			})
			errs[i] = err
		}()
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, DuplicateDeploymentErr):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
}

func TestConcurrentCreatesRejectDuplicateIngressClaim(t *testing.T) {
	h, _, _ := newV2DeploymentHandler(t)
	spec := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []*apigen.Ingress{{
			Kind:                 apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
			Hostname:             "db.example.com",
			TlsPassthroughConfig: &apigen.TlsPassthroughConfig{ContainerPort: 5432, HostPort: 8443},
		}},
	})
	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
				SpaceID: 1, Name: fmt.Sprintf("claimant-%d", i),
				NodeID: 1,
				Spec:   spec,
			})
			errs[i] = err
		}()
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case strings.Contains(err.Error(), "already claimed by another deployment"):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
}

func TestSecretMoveRacingDeploymentCreateKeepsLocality(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}

	for round := range 20 {
		sec, err := h.Secrets.Create(fmt.Sprintf("token-%d", round), []byte("v"), 0, prod.ID, 0)
		if err != nil {
			t.Fatalf("create secret: %v", err)
		}
		var wg sync.WaitGroup
		var createErr, moveErr error
		var created *apigen.Deployment
		wg.Add(2)
		go func() {
			defer wg.Done()
			created, createErr = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
				SpaceID: prod.ID, Name: fmt.Sprintf("pinner-%d", round),
				NodeID: node.ID,
				Spec:   secretEnvSpec("nginx", sec.ID),
			})
		}()
		go func() {
			defer wg.Done()
			_, moveErr = h.PostV1SecretsMove(apigen.Context{}, &apigen.SecretMoveRequest{
				SecretID: sec.SecretID,
				SpaceID:  staging.ID,
			})
		}()
		wg.Wait()

		meta, ok := h.Secrets.MetaByID(sec.ID)
		if !ok {
			t.Fatalf("round %d: secret version disappeared", round)
		}
		if createErr == nil && moveErr == nil && meta.SpaceID != prod.ID && meta.SpaceID != state.DefaultSpaceID {
			t.Fatalf("round %d: deployment %d pins secret in space %d — locality violated", round, created.ID, meta.SpaceID)
		}
	}
}
