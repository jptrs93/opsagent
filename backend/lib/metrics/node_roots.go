package metrics

import "github.com/jptrs93/opsagent/backend/ainit"

func OpenDeployRoots() NodeRoots {
	cfg := ainit.StaticConfig
	return NodeRoots{
		Data: cfg.DataDir,
		Other: []string{
			cfg.VolumesDir,
			cfg.DataDir + "-containerd",
			cfg.NixStoresDir,
			cfg.AssetCacheDir,
			cfg.ReleasesDir,
			cfg.LogArchiveDir,
			cfg.LogWALDir,
			cfg.MetricsDir,
			cfg.PrepareOutputDir,
		},
	}
}
