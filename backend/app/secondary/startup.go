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
		underlayAddress := cfg.UnderlayAddress
		if underlayAddress == "" {
			var err error
			underlayAddress, err = resolveDefaultUnderlayAddress(cfg.PrimaryClusterAddr)
			if err != nil {
				panic(err)
			}
		}
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
			UnderlayAddress:              underlayAddress,
		}); err != nil {
			panic(fmt.Sprintf("worker enrollment: %v", err))
		}
	}
	runtimeCfg := MustLoadRuntimeConfig(cfg, caPath, certPath, keyPath)
	slog.Info(fmt.Sprintf("opendeploy starting secondary version=%v nodeIdentifier=%v clusterAddr=%v primaryName=%v", version.Version, runtimeCfg.NodeIdentifier, runtimeCfg.PrimaryClusterAddr, runtimeCfg.PrimaryName))
	run(ctx, runtimeCfg)
}

func MustLoadRuntimeConfig(cfg ainit.StaticConfiguration, caPath, certPath, keyPath string) runtimeConfig {
	tlsCfg := certu.MustLoadTLSConfig(caPath, certPath, keyPath)
	nodeIdentifier := certu.MustCertLoadCommonName(certPath)
	store := sqlite.NewSecondaryStorage(filepath.Join(cfg.DataDir, "secondary.db"))
	defer store.Close()

	var netDeploymentID, nodeID int32
	for _, item := range store.FetchScheduledSnapshot(nil) {
		if sqlite.IsNetproxyDeploymentConfig(&item.Config) && item.Config.ID != 0 {
			netDeploymentID = item.Config.ID
			nodeID = item.Config.NodeID
		}
	}
	if netDeploymentID == 0 || nodeID <= 0 {
		panic("cached netproxy deployment has no valid node ID")
	}

	var prefix network.Prefix
	if _, mapPrefix, ok, err := cachedClusterNetMap(store, nodeID, network.Prefix{}); err != nil {
		panic(err)
	} else if ok {
		prefix = mapPrefix
	} else {
		b, ok := store.FetchLocalKV(sqlite.LocalKVClusterNetwork)
		if !ok {
			panic("cached cluster network is missing")
		}
		info, err := apigen.DecodeClusterNetworkInfo(b)
		if err != nil {
			panic(fmt.Sprintf("decoding cached cluster network: %v", err))
		}
		prefix, err = network.ParsePrefix(info.UlaPrefix)
		if err != nil {
			panic(fmt.Sprintf("parsing cached cluster network: %v", err))
		}
	}

	return runtimeConfig{
		TLS:                tlsCfg,
		PrimaryClusterAddr: cfg.PrimaryClusterAddr,
		PrimaryName:        cfg.PrimaryName,
		UnderlayAddress:    cfg.UnderlayAddress,
		NodeIdentifier:     nodeIdentifier,
		NodeID:             nodeID,
		DataDir:            cfg.DataDir,
		GitCacheDir:        cfg.GitCacheDir,
		ReleasesDir:        cfg.ReleasesDir,
		NetproxyStatePath:  cfg.NetproxyStatePath,
		ClusterPrefix:      prefix,
		NetDeploymentID:    netDeploymentID,
	}
}
