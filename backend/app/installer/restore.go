package installer

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primarybootstrap"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const (
	primaryConfigWebListen         = "WEB_LISTEN"
	primaryConfigWebHTTPOnly       = "WEB_HTTP_ONLY"
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

func restorePrimaryBackup(opts restoreOptions, install installOptions, own owner) error {
	dbPath := filepath.Join(dataDir, "primary.db")
	if dryRun {
		planned("restore primary database from s3://%s/%s to %s", opts.Bucket, opts.Path, dbPath)
		planned("unlock restored secrets store and write new local machine key")
		for _, override := range restoredPrimaryConfigOverrides(install) {
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

	client := s3.NewReplicaClient()
	client.AccessKeyID = opts.AccessKeyID
	client.SecretAccessKey = opts.SecretAccessKey
	client.Bucket = opts.Bucket
	client.Path = opts.Path
	client.Region = opts.Region
	client.Endpoint = opts.Endpoint
	if opts.Endpoint != "" {
		client.ForcePathStyle = true
	}

	db := litestream.NewDB(dbPath)
	replica := litestream.NewReplicaWithClient(db, client)
	restoreOpts := litestream.NewRestoreOptions()
	restoreOpts.OutputPath = tmpPath
	if err := replica.Restore(context.Background(), restoreOpts); err != nil {
		return fmt.Errorf("restore primary database: %w", err)
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("install restored primary database: %w", err)
	}
	if err := chmodChown(dbPath, 0o600, own); err != nil {
		return err
	}

	if err := unlockRestoredSecrets(dbPath, opts.RecoveryCode, own); err != nil {
		return fmt.Errorf("%w; delete %s, %s, %s, and %s before trying install recovery again", err, dbPath, dbPath+"-wal", dbPath+"-shm", filepath.Join(dataDir, "machine.key"))
	}
	if err := (primarybootstrap.Service{DataDir: dataDir}).MigrateAndValidate(context.Background()); err != nil {
		return fmt.Errorf("migrating restored primary bootstrap state: %w", err)
	}
	if err := applyRestoredPrimaryConfigOverrides(dbPath, install, own); err != nil {
		return err
	}
	info("restored primary database and re-established local machine key")
	return nil
}

type restoredPrimaryConfigOverride struct {
	key       string
	value     string
	sensitive bool
}

func (o restoredPrimaryConfigOverride) displayValue() string {
	if o.sensitive {
		return "<redacted>"
	}
	return o.value
}

func restoredPrimaryConfigOverrides(opts installOptions) []restoredPrimaryConfigOverride {
	overrides := []restoredPrimaryConfigOverride{}
	if opts.webListen != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigWebListen, value: *opts.webListen})
	}
	if opts.httpOnly != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigWebHTTPOnly, value: strconv.FormatBool(*opts.httpOnly)})
	}
	if opts.webTLSSelfManaged != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigWebTLSSelfManaged, value: strconv.FormatBool(*opts.webTLSSelfManaged)})
	}
	if opts.webTLSCertPEM != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigWebTLSCertPEM, value: *opts.webTLSCertPEM, sensitive: true})
	}
	if opts.clusterListen != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigClusterListen, value: *opts.clusterListen})
	}
	if opts.enrollmentListen != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigEnrollmentListen, value: *opts.enrollmentListen})
	}
	if opts.acmeHosts != nil {
		overrides = append(overrides, restoredPrimaryConfigOverride{key: primaryConfigAcmeHosts, value: *opts.acmeHosts})
	}
	return overrides
}

func applyRestoredPrimaryConfigOverrides(dbPath string, opts installOptions, own owner) error {
	overrides := restoredPrimaryConfigOverrides(opts)
	if len(overrides) == 0 {
		return nil
	}
	store := sqlite.NewPrimaryStorage(dbPath)
	service, err := config.NewService(store)
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
			settings.HttpWeb.Listen = apigen.StringSetting{Value: override.value}
			settings.HttpsWeb.Listen = apigen.StringSetting{Value: override.value}
		case primaryConfigWebHTTPOnly:
			httpOnly, _ := strconv.ParseBool(override.value)
			settings.HttpWeb.Enabled = apigen.BoolSetting{Value: httpOnly}
			settings.HttpsWeb.Enabled = apigen.BoolSetting{Value: !httpOnly}
		case primaryConfigWebTLSSelfManaged:
			selfManaged, _ := strconv.ParseBool(override.value)
			settings.HttpsWeb.TlsSelfManaged = apigen.BoolSetting{Value: selfManaged}
		case primaryConfigWebTLSCertPEM:
			bundle := []byte(override.value)
			if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
				_ = store.Close()
				return fmt.Errorf("restored Web TLS certificate PEM must contain a certificate chain and private key: %w", err)
			}
			meta, err := secretsMgr.Set(secrets.TLSCertPEMSecretName, bundle, 0)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("creating restored Web TLS certificate secret: %w", err)
			}
			settings.HttpsWeb.TlsSelfManaged = apigen.BoolSetting{Value: true}
			settings.HttpsWeb.TlsCertPem = apigen.SecretRef{ID: meta.ID}
		case primaryConfigClusterListen:
			settings.Cluster.Listen = apigen.StringSetting{Value: override.value}
		case primaryConfigEnrollmentListen:
			settings.Cluster.EnrollmentListen = apigen.StringSetting{Value: override.value}
		case primaryConfigAcmeHosts:
			settings.HttpsWeb.AcmeHosts = apigen.StringSetting{Value: override.value}
		}
	}
	if service.MustLoadConfigBoolValue(settings.HttpsWeb.Enabled) && service.MustLoadConfigBoolValue(settings.HttpsWeb.TlsSelfManaged) && settings.HttpsWeb.TlsCertPem.ID == 0 {
		if secretsMgr == nil {
			secretsMgr, err = secrets.Open(dataDir, store)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("open restored secrets store: %w", err)
			}
		}
		acmeHosts := service.MustLoadConfigStringValue(settings.HttpsWeb.AcmeHosts)
		listen := service.MustLoadConfigStringValue(settings.HttpsWeb.Listen)
		if _, err := certu.BootstrapWebUISelfSigned(secretsMgr, certu.WebUISelfSignedNames(acmeHosts, listen)); err != nil {
			_ = store.Close()
			return fmt.Errorf("creating restored self-managed Web TLS certificate: %w", err)
		}
	}
	if err := service.UpdateSettingsInternal(settings); err != nil {
		_ = store.Close()
		return fmt.Errorf("set restored primary config overrides: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database after config overrides: %w", err)
	}
	for _, path := range sqliteArtifactPaths(dbPath) {
		if err := chownIfExists(path, own); err != nil {
			return err
		}
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

func unlockRestoredSecrets(dbPath, recoveryCode string, own owner) error {
	store := sqlite.NewPrimaryStorage(dbPath)

	mgr, err := secrets.Open(dataDir, store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open restored secrets store: %w", err)
	}
	if err := mgr.Unlock(recoveryCode); err != nil {
		_ = store.Close()
		return fmt.Errorf("unlock restored secrets store: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database: %w", err)
	}
	for _, path := range append(sqliteArtifactPaths(dbPath), filepath.Join(dataDir, "machine.key")) {
		if err := chownIfExists(path, own); err != nil {
			return err
		}
	}
	return nil
}

func chmodChown(path string, mode os.FileMode, own owner) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return own.apply(path)
}

func chownIfExists(path string, own owner) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return own.apply(path)
}
