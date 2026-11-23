package installer

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/app/primarybootstrap"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const (
	primaryConfigWebListen         = "WEB_LISTEN"
	primaryConfigWebHTTPOnly       = "WEB_HTTP_ONLY"
	primaryConfigRequirePasskey    = "REQUIRE_PASSKEY"
	primaryConfigWebTLSSelfManaged = "WEB_TLS_SELF_MANAGED"
	primaryConfigWebTLSCertPEM     = "WEB_TLS_CERT_PEM"
	primaryConfigClusterListen     = "CLUSTER_LISTEN"
	primaryConfigEnrollmentListen  = "ENROLLMENT_LISTEN"
	primaryConfigAcmeHosts         = "ACME_HOSTS"
)

type restoreOptions struct {
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
	RecoveryCode    string
}

func (o restoreOptions) validate() error {
	fields := []struct {
		flag  string
		value string
	}{
		{"--restore-s3-access-key-id", o.AccessKeyID},
		{"--restore-s3-secret-access-key", o.SecretAccessKey},
		{"--restore-s3-bucket", o.Bucket},
		{"--restore-s3-path", o.Path},
		{"--restore-s3-region", o.Region},
		{"--recovery-code", o.RecoveryCode},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("%s is required when --restore-backup true", field.flag)
		}
		if err := validateInstallStringFlag(field.flag, field.value); err != nil {
			return err
		}
	}
	if o.Endpoint != "" {
		if err := validateInstallStringFlag("--restore-s3-endpoint", o.Endpoint); err != nil {
			return err
		}
	}
	return nil
}

func restorePrimaryBackup(opts restoreOptions, install installOptions) error {
	dbPath := filepath.Join(dataDir, "primary.db")
	if dryRun {
		planned("restore primary database from s3://%s/%s to %s", opts.Bucket, opts.Path, dbPath)
		planned("unlock restored secrets store and write new local machine key")
		planned("invalidate restored runtime state for the replacement primary node")
		for _, override := range restoredSystemConfigOverrides(install) {
			planned("set restored primary config %s=%s", override.key, override.displayValue())
		}
		return nil
	}

	if err := ensureNoExistingPrimaryDB(dbPath); err != nil {
		return err
	}

	tmpPath := filepath.Join(dataDir, ".primary.db.restore")
	cleanupRestoreArtifacts(tmpPath)
	defer cleanupRestoreArtifacts(tmpPath)

	restoreCfg := backup.S3Config{
		AccessKeyID:     opts.AccessKeyID,
		SecretAccessKey: opts.SecretAccessKey,
		Bucket:          opts.Bucket,
		Path:            opts.Path,
		Region:          opts.Region,
		Endpoint:        opts.Endpoint,
	}
	if err := backup.Restore(context.Background(), restoreCfg, dbPath, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("install restored primary database: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return err
	}

	if err := unlockRestoredSecrets(dbPath, opts.RecoveryCode); err != nil {
		return fmt.Errorf("%w; delete %s, %s, %s, and %s before trying install recovery again", err, dbPath, dbPath+"-wal", dbPath+"-shm", filepath.Join(dataDir, "machine.key"))
	}
	if err := (primarybootstrap.Service{DataDir: dataDir}).Validate(context.Background()); err != nil {
		return fmt.Errorf("validating restored primary bootstrap state: %w", err)
	}
	if err := applyRestoredSystemConfigOverrides(dbPath, install); err != nil {
		return err
	}
	if err := invalidateRestoredPrimaryRuntimeState(dbPath); err != nil {
		return err
	}
	info("restored primary database and re-established local machine key")
	return nil
}

func invalidateRestoredPrimaryRuntimeState(dbPath string) error {
	store := state.Open(dbPath)
	nodeID, err := nodes.PrimaryNodeID(store.Queries())
	if err == nil {
		count, err := nodes.InvalidateNodeRuntimeState(store, nodeID)
		if err == nil {
			info("invalidated runtime state for %d replacement-primary deployments", count)
		}
	}
	if err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database after runtime invalidation: %w", err)
	}
	return nil
}

type restoredSystemConfigOverride struct {
	key       string
	value     string
	sensitive bool
}

func (o restoredSystemConfigOverride) displayValue() string {
	if o.sensitive {
		return "<redacted>"
	}
	return o.value
}

func restoredSystemConfigOverrides(opts installOptions) []restoredSystemConfigOverride {
	overrides := []restoredSystemConfigOverride{}
	if opts.webListen != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigWebListen, value: *opts.webListen})
	}
	if opts.httpOnly != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigWebHTTPOnly, value: strconv.FormatBool(*opts.httpOnly)})
	}
	if opts.requirePasskey != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigRequirePasskey, value: strconv.FormatBool(*opts.requirePasskey)})
	}
	if opts.selfManagedTLS() {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigWebTLSSelfManaged, value: "true"})
	}
	if opts.webTLSCertPEM != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigWebTLSCertPEM, value: *opts.webTLSCertPEM, sensitive: true})
	}
	if opts.clusterListen != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigClusterListen, value: *opts.clusterListen})
	}
	if opts.enrollmentListen != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigEnrollmentListen, value: *opts.enrollmentListen})
	}
	if opts.webHosts != nil {
		overrides = append(overrides, restoredSystemConfigOverride{key: primaryConfigAcmeHosts, value: *opts.webHosts})
	}
	return overrides
}

func applyRestoredSystemConfigOverrides(dbPath string, opts installOptions) error {
	overrides := restoredSystemConfigOverrides(opts)
	if len(overrides) == 0 {
		return nil
	}
	store := state.Open(dbPath)
	service, err := systemconfig.NewService(store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("init config service: %w", err)
	}
	var secretsMgr *secrets.Manager
	if opts.webTLSCertPEM != nil {
		secretsMgr, err = secrets.Open(dataDir, store)
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("open restored secrets store: %w", err)
		}
	}
	settings := service.Snapshot().Settings
	for _, override := range overrides {
		switch override.key {
		case primaryConfigWebListen:
			settings.HttpWeb.Listen = systemconfig.StringLiteral(override.value)
			settings.HttpsWeb.Listen = systemconfig.StringLiteral(override.value)
		case primaryConfigWebHTTPOnly:
			httpOnly, _ := strconv.ParseBool(override.value)
			settings.HttpWeb.Enabled = systemconfig.BoolLiteral(httpOnly)
			settings.HttpsWeb.Enabled = systemconfig.BoolLiteral(!httpOnly)
		case primaryConfigRequirePasskey:
			required, _ := strconv.ParseBool(override.value)
			settings.Auth.RequirePasskey = systemconfig.BoolLiteral(required)
		case primaryConfigWebTLSSelfManaged:
			selfManaged, _ := strconv.ParseBool(override.value)
			settings.HttpsWeb.TlsSelfManaged = systemconfig.BoolLiteral(selfManaged)
		case primaryConfigWebTLSCertPEM:
			bundle := []byte(override.value)
			if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
				_ = store.Close()
				return fmt.Errorf("restored Web TLS certificate PEM must contain a certificate chain and private key: %w", err)
			}
			meta, err := secretsMgr.SetByName(secrets.TLSCertPEMSecretName, bundle, 0)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("creating restored Web TLS certificate secret: %w", err)
			}
			settings.HttpsWeb.TlsSelfManaged = systemconfig.BoolLiteral(true)
			settings.HttpsWeb.TlsCertPem = apigen.Some(meta.Ref().Secret())
		case primaryConfigClusterListen:
			settings.Cluster.Listen = systemconfig.StringLiteral(override.value)
		case primaryConfigEnrollmentListen:
			settings.Cluster.EnrollmentListen = systemconfig.StringLiteral(override.value)
		case primaryConfigAcmeHosts:
			settings.HttpsWeb.AcmeHosts = systemconfig.StringLiteral(override.value)
		}
	}
	if service.MustLoadBoolSetting(settings.HttpsWeb.Enabled) && service.MustLoadBoolSetting(settings.HttpsWeb.TlsSelfManaged) && !(settings.HttpsWeb.TlsCertPem.Present && settings.HttpsWeb.TlsCertPem.Value.Valid()) {
		if secretsMgr == nil {
			secretsMgr, err = secrets.Open(dataDir, store)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("open restored secrets store: %w", err)
			}
		}
		acmeHosts := service.MustLoadStringSetting(settings.HttpsWeb.AcmeHosts)
		listen := service.MustLoadStringSetting(settings.HttpsWeb.Listen)
		_, caCertPEM, err := pki.EnsureWebUILocalTLS(secretsMgr, certu.WebUITLSNames(acmeHosts, listen))
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("creating restored self-managed Web TLS certificate: %w", err)
		}
		if err := certu.WriteWebUILocalCAFile(dataDir, caCertPEM); err != nil {
			_ = store.Close()
			return fmt.Errorf("exporting restored Web UI CA certificate: %w", err)
		}
	}
	if err := service.UpdateSettingsInternal(settings); err != nil {
		_ = store.Close()
		return fmt.Errorf("set restored primary config overrides: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database after config overrides: %w", err)
	}
	info("updated restored primary listener config")
	return nil
}

func ensureNoExistingPrimaryDB(dbPath string) error {
	for _, path := range sqliteArtifactPaths(dbPath) {
		if pathExists(path) {
			return fmt.Errorf("refusing to restore backup because %s already exists; delete %s, %s, %s, and %s before trying install recovery again", path, dbPath, dbPath+"-wal", dbPath+"-shm", filepath.Join(dataDir, "machine.key"))
		}
	}
	return nil
}

func cleanupRestoreArtifacts(dbPath string) {
	for _, path := range sqliteArtifactPaths(dbPath) {
		_ = os.Remove(path)
	}
}

func sqliteArtifactPaths(dbPath string) []string {
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
}

func unlockRestoredSecrets(dbPath, recoveryCode string) error {
	store := state.Open(dbPath)

	mgr, err := secrets.Open(dataDir, store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open restored secrets store: %w", err)
	}
	if err := mgr.Unlock(recoveryCode, 0); err != nil {
		_ = store.Close()
		return fmt.Errorf("unlock restored secrets store: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database: %w", err)
	}
	return nil
}
