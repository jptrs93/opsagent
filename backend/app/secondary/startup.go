package secondary

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/version"
)

// Run enrolls the secondary when needed, loads its cluster identity, and starts
// the local deployment operator and primary connection.
func Run(ctx context.Context) {
	cfg := ainit.StaticConfig
	if cfg.PrimaryClusterAddr == "" || cfg.PrimaryEnrollmentAddr == "" {
		panic("OPENDEPLOY_PRIMARY_CLUSTER_ADDR and OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR must be set when running secondary")
	}
	caPath, certPath, keyPath := certu.WorkerTLSPaths(cfg.TLSDir)
	if !certu.WorkerTLSMaterialExists(caPath, certPath, keyPath) {
		if cfg.PrimaryEnrollmentFingerprint == "" {
			panic("OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT must be set before worker enrollment")
		}
		slog.Info(fmt.Sprintf("worker cluster certs missing; starting enrollment enrollmentAddr=%v", cfg.PrimaryEnrollmentAddr))
		if err := Enroll(ctx, EnrollmentConfig{
			PrimaryEnrollmentAddr:        cfg.PrimaryEnrollmentAddr,
			PrimaryEnrollmentFingerprint: cfg.PrimaryEnrollmentFingerprint,
			DataDir:                      cfg.DataDir,
			ClusterCAPath:                caPath,
			ClusterCertPath:              certPath,
			ClusterKeyPath:               keyPath,
			OpendeployVersion:            version.Version,
		}); err != nil {
			panic(fmt.Sprintf("worker enrollment: %v", err))
		}
	}
	runtimeCfg := MustLoadRuntimeConfig(cfg, caPath, certPath, keyPath)
	slog.Info(fmt.Sprintf("opendeploy starting secondary version=%v machine=%v clusterAddr=%v primaryName=%v", version.Version, runtimeCfg.MachineName, runtimeCfg.PrimaryClusterAddr, runtimeCfg.PrimaryName))
	run(ctx, runtimeCfg)
}

func MustLoadRuntimeConfig(cfg ainit.StaticConfiguration, caPath, certPath, keyPath string) runtimeConfig {
	tlsCfg := certu.MustLoadTLSConfig(caPath, certPath, keyPath)
	machineName := certu.MustCertLoadCommonName(certPath)
	store := sqlite.NewSecondaryStorage(filepath.Join(cfg.DataDir, "secondary.db"))

	b, ok := store.FetchLocalKV(sqlite.LocalKVClusterNetwork)
	if !ok {
		panic("cached cluster network is missing")
	}
	info, err := apigen.DecodeClusterNetworkInfo(b)
	if err != nil {
		panic(fmt.Sprintf("decoding cached cluster network: %v", err))
	}
	prefix, err := network.ParsePrefix(info.UlaPrefix)
	if err != nil {
		panic(fmt.Sprintf("parsing cached cluster network: %v", err))
	}

	var netDeploymentID int32
	for _, item := range store.FetchDeploymentSnapshot(nil) {
		if item.Config.ConfigID.Machine != machineName {
			continue
		}
		if sqlite.IsNetproxyDeploymentConfig(&item.Config) && item.Config.ID != 0 {
			netDeploymentID = item.Config.ID
		}
	}
	if netDeploymentID == 0 {
		panic(fmt.Sprintf("cached netproxy deployment is missing for machine %q", machineName))
	}

	return runtimeConfig{
		TLS:                tlsCfg,
		PrimaryClusterAddr: cfg.PrimaryClusterAddr,
		PrimaryName:        cfg.PrimaryName,
		MachineName:        machineName,
		DataDir:            cfg.DataDir,
		GitCacheDir:        cfg.GitCacheDir,
		ReleasesDir:        cfg.ReleasesDir,
		NetproxyStatePath:  cfg.NetproxyStatePath,
		ClusterPrefix:      prefix,
		NetDeploymentID:    netDeploymentID,
	}
}
