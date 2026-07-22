package ainit

import (
	"io"
	"log/slog"
	"os"
	"path"

	"github.com/jptrs93/goutil/envu"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/fileu"
	"github.com/jptrs93/opsagent/backend/lib/log"
)

var StaticConfig StaticConfiguration

const productionDataDir = "/var/lib/opendeploy"

func init() {
	initArgs()
	if Args.Installer || Args.Command == CommandRawLogConsumer {
		return
	}
	StaticConfig = envu.MustParse[StaticConfiguration](os.LookupEnv)
	if envu.IsTestBasedOnArgs() {
		ensureStaticDirs(Args.Command, &StaticConfig, path.Join(erru.Must(os.UserConfigDir()), "opendeploy"))
	} else {
		ensureStaticDirs(Args.Command, &StaticConfig, productionDataDir)
	}
	var w io.WriteCloser
	if Args.Command == CommandNetproxy {
		w = os.Stdout
	} else {
		// Main opendeploy agents (primary, secondary) are a special case that write logs directly to /var/opendeploy-run-logs/0
		basePath := log.SystemLogBasePath(StaticConfig.RunOutputDir)
		w = erru.Must(log.NewSystemLogWriter(basePath))
	}
	logLevel := envu.MustGetOrDefault[slog.Level]("LOG_LEVEL", slog.LevelInfo)
	slog.SetDefault(slog.New(log.NewSlogHandler(w, logLevel)))
}

func ensureStaticDirs(command Command, cfg *StaticConfiguration, dataDir string) {
	cfg.DataDir = dataDir
	cfg.AssetCacheDir = dataDir + "-assets"
	cfg.VolumesDir = dataDir + "-volumes"
	cfg.ReleasesDir = dataDir + "-releases"
	cfg.PrepareOutputDir = dataDir + "-build-logs"
	cfg.RunOutputDir = dataDir + "-run-logs"
	cfg.LargeAssetsDir = path.Join(dataDir, "large-assets")
	cfg.GitCacheDir = path.Join(dataDir, "git-cache")
	cfg.GitMetadataDir = path.Join(cfg.GitCacheDir, "metadata")
	cfg.GitWorktreesDir = path.Join(cfg.GitCacheDir, "worktrees")
	cfg.NetproxyDir = path.Join(dataDir, "netproxy")
	cfg.NetproxyStatePath = path.Join(cfg.NetproxyDir, "netstate.pb")
	cfg.ReadinessDir = path.Join(dataDir, "readiness")
	cfg.ResolvConfDir = path.Join(dataDir, "resolvconf")
	cfg.TLSDir = path.Join(dataDir, "tls")
	cfg.ACMECacheDir = path.Join(dataDir, ".certs")
	cfg.CtrdNamespace = "opendeploy"
	cfg.CtrdAddress = "/run/opendeploy/containerd.sock"
	if command != CommandPrimary && command != CommandSecondary && command != commandTest {
		return
	}
	fileu.MustEnsureDirWithPerm(cfg.DataDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.AssetCacheDir, 0o755)
	fileu.MustEnsureDirWithPerm(cfg.PrepareOutputDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.RunOutputDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.VolumesDir, 0o755)
	fileu.MustEnsureDirWithPerm(cfg.ReleasesDir, 0o755)
	fileu.MustEnsureDirWithPerm(cfg.LargeAssetsDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.GitCacheDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.GitMetadataDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.GitWorktreesDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.NetproxyDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.ReadinessDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.ResolvConfDir, 0o755)
	fileu.MustEnsureDirWithPerm(cfg.TLSDir, 0o750)
	fileu.MustEnsureDirWithPerm(cfg.ACMECacheDir, 0o700)
}

type StaticConfiguration struct {
	DataDir           string
	AssetCacheDir     string
	RunOutputDir      string
	PrepareOutputDir  string
	VolumesDir        string
	ReleasesDir       string
	LargeAssetsDir    string
	GitCacheDir       string
	GitMetadataDir    string
	GitWorktreesDir   string
	NetproxyDir       string
	NetproxyStatePath string
	ReadinessDir      string
	ResolvConfDir     string
	TLSDir            string
	ACMECacheDir      string
	CtrdAddress       string
	CtrdNamespace     string

	PrimaryClusterAddr           string `env:"OPENDEPLOY_PRIMARY_CLUSTER_ADDR,"`           // secondaries only: primary's mTLS cluster address
	PrimaryEnrollmentAddr        string `env:"OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR,"`        // secondaries only: primary's unauthenticated enrollment address
	PrimaryEnrollmentFingerprint string `env:"OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT,"` // secondaries only: sha256 SPKI fingerprint for enrollment TLS pinning
	PrimaryName                  string `env:"OPENDEPLOY_PRIMARY_NAME,primary"`            // primary cert DNS name; secondaries use it for TLS verification when dialing by IP
	UnderlayAddress              string `env:"OPENDEPLOY_UNDERLAY_ADDRESS,"`               // optional; derived from cluster connectivity when empty

	PasskeyExtraOrigins []string `env:"OPENDEPLOY_PASSKEY_EXTRA_ORIGINS,"`
}
