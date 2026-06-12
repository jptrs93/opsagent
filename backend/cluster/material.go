package cluster

import (
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const (
	secretCACert      = "opendeploy.cluster.ca.cert"
	secretCAKey       = "opendeploy.cluster.ca.key"
	secretPrimaryCert = "opendeploy.cluster.primary.cert"
	secretPrimaryKey  = "opendeploy.cluster.primary.key"
)

type Material struct {
	CACert      []byte
	CAKey       []byte
	PrimaryCert []byte
	PrimaryKey  []byte
}

func BootstrapPrimary(store *secrets.Manager, primaryName string) (*Material, error) {
	mat, err := LoadPrimary(store)
	if err == nil {
		return mat, nil
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		return nil, err
	}
	caCert, caKey, err := certu.GenerateClusterCA("opendeploy-cluster")
	if err != nil {
		return nil, err
	}
	primaryCert, primaryKey, err := certu.GenerateNodeCertificate(caCert, caKey, primaryName)
	if err != nil {
		return nil, err
	}
	mat = &Material{CACert: caCert, CAKey: caKey, PrimaryCert: primaryCert, PrimaryKey: primaryKey}
	if err := store.SetInternal(secretCACert, mat.CACert); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretCAKey, mat.CAKey); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretPrimaryCert, mat.PrimaryCert); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretPrimaryKey, mat.PrimaryKey); err != nil {
		return nil, err
	}
	return mat, nil
}

func LoadPrimary(store *secrets.Manager) (*Material, error) {
	caCert, err := store.RevealInternal(secretCACert)
	if err != nil {
		return nil, err
	}
	caKey, err := store.RevealInternal(secretCAKey)
	if err != nil {
		return nil, err
	}
	primaryCert, err := store.RevealInternal(secretPrimaryCert)
	if err != nil {
		return nil, err
	}
	primaryKey, err := store.RevealInternal(secretPrimaryKey)
	if err != nil {
		return nil, err
	}
	return &Material{CACert: caCert, CAKey: caKey, PrimaryCert: primaryCert, PrimaryKey: primaryKey}, nil
}

func SignWorkerCertificateRequest(store *secrets.Manager, csrPEM []byte, workerName string) (caCert, workerCert []byte, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, fmt.Errorf("loading cluster signing material: %w", err)
	}
	return certu.SignWorkerCertificateRequestFromPEM(mat.CACert, mat.CAKey, csrPEM, workerName)
}
