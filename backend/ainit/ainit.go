package ainit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"

	"github.com/jptrs93/goutil/envu"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/logconsumer"
	"github.com/jptrs93/opsagent/backend/util/secretu"
	"github.com/jptrs93/opsagent/backend/version"
)

var StaticConfig StaticConfiguration

const productionDataDir = "/var/lib/opendeploy"

func init() {
	initArgs()
	if Args.Installer || Args.Command == CommandSplitLogConsumer || Args.Command == CommandRawLogConsumer {
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
	mustCreateDir(StaticConfig.DataDir, 0o750)
	mustCreateDir(StaticConfig.LogDir, 0o750)
	mustCreateDir(StaticConfig.PrepareOutputDir, 0o750)
	mustCreateDir(StaticConfig.RunOutputDir, 0o750)
	mustCreateDir(StaticConfig.VolumesDir, 0o755)
	mustCreateDir(StaticConfig.ReleasesDir, 0o755)
	basePath := logconsumer.SystemLogBasePath(StaticConfig.RunOutputDir, version.Version)
	w, err := logconsumer.NewHourlyWriter(basePath)
	if err != nil {
		panic(fmt.Sprintf("opening service log writer: %v", err))
	}
	logLevel := getLogLevel()
	l := slog.New(logconsumer.NewSlogHandler(w, logLevel))
	slog.SetDefault(l)
}

func mustCreateDir(p string, mode os.FileMode) {
	if err := os.MkdirAll(p, mode); err != nil {
		panic(fmt.Sprintf("creating dir %q: %v", p, err))
	}
	if err := os.Chmod(p, mode); err != nil {
		panic(fmt.Sprintf("chmod dir %q: %v", p, err))
	}
}

func getLogLevel() slog.Level {
	logLevelStr := envu.MustGetOrDefault[string]("LOG_LEVEL", "INFO")
	var level slog.Level
	err := json.Unmarshal([]byte(fmt.Sprintf("\"%s\"", logLevelStr)), &level)
	if err == nil {
		return level
	}
	slog.Warn(fmt.Sprintf("decoding log level '%v': %v", logLevelStr, err))
	return slog.LevelInfo
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

	PrimaryClusterAddr    string `env:"OPENDEPLOY_PRIMARY_CLUSTER_ADDR,"`    // secondaries only: primary's mTLS cluster address
	PrimaryEnrollmentAddr string `env:"OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR,"` // secondaries only: primary's unauthenticated enrollment address
	PrimaryName           string `env:"OPENDEPLOY_PRIMARY_NAME,primary"`     // primary cert DNS name; secondaries use it for TLS verification when dialing by IP

	InitialWebListen          string   `env:"OPENDEPLOY_INITIAL_WEB_LISTEN,:443"`
	InitialWebHTTPOnly        bool     `env:"OPENDEPLOY_INITIAL_WEB_HTTP_ONLY,false"` // serve the primary API over plain HTTP on port 8080 instead of ACME TLS on port 443
	InitialMasterPasswordHash string   `env:"OPENDEPLOY_INITIAL_MASTER_PASSWORD_HASH,"`
	InitialClusterListen      string   `env:"OPENDEPLOY_INITIAL_CLUSTER_LISTEN,:9443"`    // mTLS listen address
	InitialEnrollmentListen   string   `env:"OPENDEPLOY_INITIAL_ENROLLMENT_LISTEN,:9444"` // HTTPS worker enrollment listen address
	InitialAcmeHosts          []string `env:"OPENDEPLOY_INITIAL_ACME_HOSTS,opendeploy.dev"`
	InitialAcmeEmail          string   `env:"OPENDEPLOY_INITIAL_ACME_EMAIL,"`
}

type DynamicConfiguration struct {
	WebListen        string
	WebHTTPOnly      bool
	ClusterListen    string
	EnrollmentListen string
	AcmeHosts        []string
	AcmeEmail        string

	GithubToken secretu.SecretValue

	BackupEnabled           bool
	BackupS3AccessKeyID     string
	BackupS3SecretAccessKey secretu.SecretValue
	BackupS3Bucket          string
	BackupS3Path            string
	BackupS3Region          string
	BackupS3Endpoint        string

	LargeAssetS3Enabled         bool
	LargeAssetS3AccessKeyID     string
	LargeAssetS3SecretAccessKey secretu.SecretValue
	LargeAssetS3Bucket          string
	LargeAssetS3Path            string
	LargeAssetS3Region          string
	LargeAssetS3Endpoint        string
}
