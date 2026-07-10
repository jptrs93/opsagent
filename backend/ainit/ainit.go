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
		StaticConfig.DataDir = path.Join(erru.Must(os.UserConfigDir()), "opendeploy")
	} else {
		StaticConfig.DataDir = productionDataDir
	}
	StaticConfig.VolumesDir = StaticConfig.DataDir + "-volumes"
	StaticConfig.ReleasesDir = StaticConfig.DataDir + "-releases"
	StaticConfig.PrepareOutputDir = StaticConfig.DataDir + "-build-logs"
	StaticConfig.RunOutputDir = StaticConfig.DataDir + "-run-logs"
	StaticConfig.LogDir = path.Join(StaticConfig.DataDir, "log")
	StaticConfig.ContainerdNamespace = "opendeploy"
	StaticConfig.ContainerdAddress = "/run/opendeploy/containerd.sock"
	fileu.MustEnsureDirWithPerm(StaticConfig.DataDir, 0o750)
	fileu.MustEnsureDirWithPerm(StaticConfig.LogDir, 0o750)
	fileu.MustEnsureDirWithPerm(StaticConfig.PrepareOutputDir, 0o750)
	fileu.MustEnsureDirWithPerm(StaticConfig.RunOutputDir, 0o750)
	fileu.MustEnsureDirWithPerm(StaticConfig.VolumesDir, 0o755)
	fileu.MustEnsureDirWithPerm(StaticConfig.ReleasesDir, 0o755)
	var w io.WriteCloser
	if Args.Command == CommandDataplane {
		w = os.Stdout
	} else {
		// Main opendeploy agents (primary, secondary) are a special case that write logs directly to /var/opendeploy-run-logs/0
		basePath := log.SystemLogBasePath(StaticConfig.RunOutputDir)
		w = erru.Must(log.NewSystemLogWriter(basePath))
	}
	logLevel := envu.MustGetOrDefault[slog.Level]("LOG_LEVEL", slog.LevelInfo)
	slog.SetDefault(slog.New(log.NewSlogHandler(w, logLevel)))
}

type StaticConfiguration struct {
	DataDir             string
	RunOutputDir        string
	PrepareOutputDir    string
	VolumesDir          string
	ReleasesDir         string
	LogDir              string
	ContainerdAddress   string
	ContainerdNamespace string

	PrimaryClusterAddr           string `env:"OPENDEPLOY_PRIMARY_CLUSTER_ADDR,"`           // secondaries only: primary's mTLS cluster address
	PrimaryEnrollmentAddr        string `env:"OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR,"`        // secondaries only: primary's unauthenticated enrollment address
	PrimaryEnrollmentFingerprint string `env:"OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT,"` // secondaries only: sha256 SPKI fingerprint for enrollment TLS pinning
	PrimaryName                  string `env:"OPENDEPLOY_PRIMARY_NAME,primary"`            // primary cert DNS name; secondaries use it for TLS verification when dialing by IP

	InitialWebHTTPEnabled     bool     `env:"OPENDEPLOY_INITIAL_WEB_HTTP_ENABLED,false"`
	InitialWebHTTPListen      string   `env:"OPENDEPLOY_INITIAL_WEB_HTTP_LISTEN,:8080"`
	InitialWebHTTPSEnabled    bool     `env:"OPENDEPLOY_INITIAL_WEB_HTTPS_ENABLED,true"`
	InitialWebHTTPSListen     string   `env:"OPENDEPLOY_INITIAL_WEB_HTTPS_LISTEN,:443"`
	InitialWebTLSSelfManaged  bool     `env:"OPENDEPLOY_INITIAL_WEB_TLS_SELF_MANAGED,false"`
	InitialWebTLSCertPEM      string   `env:"OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM,"`
	InitialWebTLSCertPEMFile  string   `env:"OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM_FILE,"`
	InitialMasterPasswordHash string   `env:"OPENDEPLOY_INITIAL_MASTER_PASSWORD_HASH,"`
	PasskeyExtraOrigins       []string `env:"OPENDEPLOY_PASSKEY_EXTRA_ORIGINS,"`
	InitialClusterListen      string   `env:"OPENDEPLOY_INITIAL_CLUSTER_LISTEN,:9443"`    // mTLS listen address
	InitialEnrollmentListen   string   `env:"OPENDEPLOY_INITIAL_ENROLLMENT_LISTEN,:9444"` // HTTPS worker enrollment listen address
	InitialAcmeHosts          []string `env:"OPENDEPLOY_INITIAL_ACME_HOSTS,opendeploy.dev"`
	InitialAcmeEmail          string   `env:"OPENDEPLOY_INITIAL_ACME_EMAIL,"`
}
