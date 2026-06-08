package ainit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/jptrs93/goutil/envu"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
)

var Config Configuration

const productionDataDir = "/var/lib/opendeploy"

func init() {
	if isInstallerInvocation() {
		return
	}
	Config = envu.MustParse[Configuration](os.LookupEnv)
	if isTestInvocation() {
		Config.DataDir = path.Join(erru.Must(os.UserConfigDir()), "opendeploy")
	} else {
		Config.DataDir = productionDataDir
	}
	Config.VolumesDir = Config.DataDir + "-volumes"
	Config.ReleasesDir = Config.DataDir + "-releases"
	Config.LogDir = path.Join(Config.DataDir, "log")
	Config.PrepareOutputDir = path.Join(Config.DataDir, "prepare")
	Config.RunOutputDir = path.Join(Config.DataDir, "runs")
	mustCreateDir(Config.DataDir, 0o750)
	mustCreateDir(Config.LogDir, 0o750)
	mustCreateDir(Config.PrepareOutputDir, 0o750)
	mustCreateDir(Config.RunOutputDir, 0o750)
	mustCreateDir(Config.VolumesDir, 0o755)
	mustCreateDir(Config.ReleasesDir, 0o755)
	logLevel := getLogLevel()
	l := slog.New(&logu.PlainLogHandler{Writer: os.Stdout, Level: logLevel})
	slog.SetDefault(l)
}

func isInstallerInvocation() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "install", "uninstall":
		return true
	}
	return false
}

func isTestInvocation() bool {
	return strings.HasSuffix(os.Args[0], ".test")
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

type Configuration struct {
	DataDir          string
	RunOutputDir     string
	PrepareOutputDir string
	VolumesDir       string
	ReleasesDir      string
	LogDir           string

	ContainerdAddress   string `env:"OPENDEPLOY_CONTAINERD_ADDRESS,/run/opendeploy/containerd.sock"`
	ContainerdNamespace string `env:"OPENDEPLOY_CONTAINERD_NAMESPACE,opendeploy"`

	BindAddr string `env:"OPENDEPLOY_BIND_ADDR,0.0.0.0"` // listen address (e.g. "0.0.0.0", "::", or a specific IP)
	HTTPOnly bool   `env:"OPENDEPLOY_HTTP_ONLY,false"`   // serve the primary API over plain HTTP on localhost:8080 instead of ACME TLS

	MasterPasswordHash      string   `env:"OPENDEPLOY_MASTER_PASSWORD_HASH,secret"`
	GithubToken             string   `env:"OPENDEPLOY_GITHUB_TOKEN,"`
	AcmeHosts               []string `env:"OPENDEPLOY_ACME_HOSTS,opendeploy.dev"`
	AcmeEmail               string   `env:"OPENDEPLOY_ACME_EMAIL,"`
	BackupS3AccessKeyID     string   `env:"OPENDEPLOY_BACKUP_S3_ACCESS_KEY_ID,"`
	BackupS3SecretAccessKey string   `env:"OPENDEPLOY_BACKUP_S3_SECRET_ACCESS_KEY,"`
	BackupS3Bucket          string   `env:"OPENDEPLOY_BACKUP_S3_BUCKET,"`
	BackupS3Path            string   `env:"OPENDEPLOY_BACKUP_S3_PATH,opendeploy/primary"`
	BackupS3Region          string   `env:"OPENDEPLOY_BACKUP_S3_REGION,us-east-1"`
	BackupS3Endpoint        string   `env:"OPENDEPLOY_BACKUP_S3_ENDPOINT,"`

	ClusterListen    string `env:"OPENDEPLOY_CLUSTER_LISTEN,:9443"`    // mTLS listen address
	EnrollmentListen string `env:"OPENDEPLOY_ENROLLMENT_LISTEN,:9444"` // unauthenticated worker enrollment listen address
	PrimaryAddr      string `env:"OPENDEPLOY_PRIMARY_ADDR,"`           // secondaries only: primary's mTLS/enrollment address
	PrimaryName      string `env:"OPENDEPLOY_PRIMARY_NAME,primary"`    // primary cert DNS name; secondaries use it for TLS verification when dialing by IP
}

func ResolvePrimaryMachineName() string {
	return Config.PrimaryName
}
