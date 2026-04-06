package ainit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jptrs93/goutil/envu"
	"github.com/jptrs93/goutil/logu"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Config Configuration

func init() {
	Config = envu.MustLoadConfig[Configuration]("")
	if Config.DataDir == "" {
		Config.DataDir = resolveDefaultDataDir()
	}

	mustCreateAppDirs()

	logLevel := getLogLevel()
	fmt.Println(fmt.Sprintf("log level is: '%v'", logLevel))
	fmt.Println(fmt.Sprintf("data dir is: '%v'", Config.DataDir))

	var l *slog.Logger
	if Config.IsLocalDev != "true" {
		logFileName := logu.MustResolveLogDir("opsagent") + "/server.log"
		logFile := &lumberjack.Logger{
			Filename:   logFileName,
			MaxSize:    100,
			MaxBackups: 50,
			MaxAge:     28,
			Compress:   true,
		}
		fmt.Println(fmt.Sprintf("log file is: '%v'", logFileName))
		l = slog.New(&logu.PlainLogHandler{Writer: logFile, Level: logLevel})
	} else {
		l = slog.New(&logu.PlainLogHandler{Writer: os.Stdout, Level: logLevel})
	}
	slog.SetDefault(l)
}

func mustCreateAppDirs() {
	Config.PrepareOutputDir = filepath.Join(Config.DataDir, "prepare")
	if err := os.MkdirAll(Config.PrepareOutputDir, 0o755); err != nil {
		panic(fmt.Sprintf("creating prepare dir: %v", err))
	}
	Config.RunOutputDir = filepath.Join(Config.DataDir, "runs")
	if err := os.MkdirAll(Config.RunOutputDir, 0o755); err != nil {
		panic(fmt.Sprintf("creating runs dir: %v", err))
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

func resolveDefaultDataDir() string {
	const appName = "opsagent"
	var baseDir string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Sprintf("resolving home dir: %v", err))
		}
		baseDir = filepath.Join(home, "Library", "Application Support")
	case "linux":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			baseDir = xdg
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				panic(fmt.Sprintf("resolving home dir: %v", err))
			}
			baseDir = filepath.Join(home, ".local", "share")
		}
	default:
		panic(fmt.Sprintf("unsupported OS for default data dir: %s", runtime.GOOS))
	}
	dir := filepath.Join(baseDir, appName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(fmt.Sprintf("creating data dir %s: %v", dir, err))
	}
	return dir
}

type Configuration struct {
	IsLocalDev       string `env:"OPSAGENT_LOCAL_DEV,false"`
	DataDir          string `env:"OPSAGENT_DATA_DIR,"`
	RunOutputDir     string
	PrepareOutputDir string

	BindAddr string `env:"OPSAGENT_BIND_ADDR,0.0.0.0"` // listen address (e.g. "0.0.0.0", "::", or a specific IP)

	MasterPasswordHash string   `env:"OPSAGENT_MASTER_PASSWORD_HASH,secret"`
	GithubToken        string   `env:"OPSAGENT_GITHUB_TOKEN,"`
	AcmeHosts          []string `env:"OPSAGENT_ACME_HOSTS,opsagent.dev"`
	AcmeEmail          string   `env:"OPSAGENT_ACME_EMAIL,"`

	// Cluster mTLS — if ClusterCA is empty, cluster mode is disabled.
	ClusterCA     string `env:"OPSAGENT_CLUSTER_CA,"`     // path to CA cert
	ClusterCert   string `env:"OPSAGENT_CLUSTER_CERT,"`   // path to this node's cert
	ClusterKey    string `env:"OPSAGENT_CLUSTER_KEY,"`    // path to this node's private key
	ClusterListen string `env:"OPSAGENT_CLUSTER_LISTEN,"` // mTLS listen address (e.g. ":9443")
	PrimaryAddr   string `env:"OPSAGENT_PRIMARY_ADDR,"`   // slaves only: primary's mTLS address
}
