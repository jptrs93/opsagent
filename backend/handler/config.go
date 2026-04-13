package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"gopkg.in/yaml.v3"
)

var InvalidYAMLErr = apigen.NewApiErr("", "invalid_yaml", http.StatusBadRequest)
var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)
var NoConfigErr = apigen.NewApiErr("", "no_config_found", http.StatusNotFound)

type yamlConfig struct {
	Environments []yamlEnvironment `yaml:"environments"`
}

type yamlEnvironment struct {
	Name        string           `yaml:"name"`
	Deployments []yamlDeployment `yaml:"deployments"`
}

type yamlDeployment struct {
	Name    string       `yaml:"name"`
	Machine string       `yaml:"machine"`
	Prepare *yamlPrepare `yaml:"prepare,omitempty"`
	Runner  *yamlRunner  `yaml:"runner,omitempty"`
}

type yamlPrepare struct {
	NixBuild      *yamlNixBuild      `yaml:"nixBuild,omitempty"`
	GithubRelease *yamlGithubRelease `yaml:"githubRelease,omitempty"`
}

type yamlNixBuild struct {
	Repo  string `yaml:"repo"`
	Flake string `yaml:"flake"`
}

type yamlGithubRelease struct {
	Repo  string `yaml:"repo"`
	Asset string `yaml:"asset,omitempty"`
	Tag   string `yaml:"tag,omitempty"`
}

type yamlRunner struct {
	OsProcess *yamlOsProcess `yaml:"osProcess,omitempty"`
	Systemd   *yamlSystemd   `yaml:"systemd,omitempty"`
}

type yamlOsProcess struct {
	WorkingDir string `yaml:"workingDir,omitempty"`
	RunAs      string `yaml:"runAs,omitempty"`
	Strategy   string `yaml:"strategy,omitempty"`
}

type yamlSystemd struct {
	Name    string `yaml:"name"`
	BinPath string `yaml:"binPath"`
}

// PutV1Config hands the raw yaml to the store together with a parser
// closure. The store persists the yaml + the parsed entries and the
// handler owns yaml parsing so the storage package does not need a
// yaml.v3 dependency.
func (h *Handler) PutV1Config(ctx apigen.Context, req *apigen.PutConfigRequest) (*apigen.UserConfigVersion, error) {
	return h.Store.PutDeploymentUserConfig(ctx, req.YamlContent, parseConfig)
}

// GetV1ConfigHistory returns every persisted user config version, newest
// first. The store owns the ordering.
func (h *Handler) GetV1ConfigHistory(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	versions := h.Store.FetchDeploymentUserConfigHistory()
	respond(writer, &apigen.UserConfigHistory{Versions: versions})
	return nil
}

// parseConfig parses the raw YAML into a flat slice of DeploymentConfig
// entries and runs semantic validation. On YAML syntax errors it returns
// InvalidYAMLErr; on semantic errors it returns InvalidConfigErr wrapped
// with a field-specific message.
func parseConfig(yamlContent string) ([]*apigen.DeploymentConfig, error) {
	var parsed yamlConfig
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		return nil, InvalidYAMLErr
	}
	return yamlToProto(&parsed)
}

func yamlToProto(yc *yamlConfig) ([]*apigen.DeploymentConfig, error) {
	out := make([]*apigen.DeploymentConfig, 0)
	seenKey := make(map[string]string) // env:machine:name -> where
	for envIdx, env := range yc.Environments {
		if env.Name == "" {
			return nil, invalidConfigErrf("environments[%d]: name is required", envIdx)
		}
		for depIdx, dep := range env.Deployments {
			if dep.Name == "" {
				return nil, invalidConfigErrf("environments[%d].deployments[%d]: name is required", envIdx, depIdx)
			}
			where := fmt.Sprintf("environments[%s].deployments[%s]", env.Name, dep.Name)
			if dep.Machine == "" {
				return nil, invalidConfigErrf("%s: machine is required", where)
			}
			if strings.Contains(dep.Machine, ":") {
				return nil, invalidConfigErrf("%s: machine must not contain ':'", where)
			}
			if strings.Contains(dep.Name, ":") {
				return nil, invalidConfigErrf("%s: name must not contain ':'", where)
			}
			idKey := env.Name + ":" + dep.Machine + ":" + dep.Name
			if prev, ok := seenKey[idKey]; ok {
				return nil, invalidConfigErrf("%s: duplicate env:machine:name with %s", where, prev)
			}
			seenKey[idKey] = where

			prepare, err := toPrepareConfig(dep.Prepare, where)
			if err != nil {
				return nil, err
			}
			runnerCfg, err := toRunnerConfig(dep.Runner, where)
			if err != nil {
				return nil, err
			}

			out = append(out, &apigen.DeploymentConfig{
				ConfigID: &apigen.DeploymentIdentifier{
					Environment: env.Name,
					Machine:     dep.Machine,
					Name:        dep.Name,
				},
				Spec: &apigen.DeploymentSpec{
					Prepare: prepare,
					Runner:  runnerCfg,
				},
			})
		}
	}
	return out, nil
}

func toPrepareConfig(yp *yamlPrepare, where string) (*apigen.PrepareConfig, error) {
	if yp == nil {
		return nil, invalidConfigErrf("%s: prepare is required", where)
	}
	hasNix := yp.NixBuild != nil
	hasGH := yp.GithubRelease != nil
	if !hasNix && !hasGH {
		return nil, invalidConfigErrf("%s.prepare: one of nixBuild or githubRelease must be set", where)
	}
	if hasNix && hasGH {
		return nil, invalidConfigErrf("%s.prepare: only one of nixBuild or githubRelease may be set", where)
	}
	out := &apigen.PrepareConfig{}
	if hasNix {
		if yp.NixBuild.Repo == "" {
			return nil, invalidConfigErrf("%s.prepare.nixBuild: repo is required", where)
		}
		if yp.NixBuild.Flake == "" {
			return nil, invalidConfigErrf("%s.prepare.nixBuild: flake is required", where)
		}
		out.NixBuild = &apigen.NixBuildConfig{
			Repo:  yp.NixBuild.Repo,
			Flake: yp.NixBuild.Flake,
		}
	}
	if hasGH {
		if yp.GithubRelease.Repo == "" {
			return nil, invalidConfigErrf("%s.prepare.githubRelease: repo is required", where)
		}
		out.GithubRelease = &apigen.GithubReleaseConfig{
			Repo:  yp.GithubRelease.Repo,
			Asset: yp.GithubRelease.Asset,
			Tag:   yp.GithubRelease.Tag,
		}
	}
	return out, nil
}

func toRunnerConfig(yr *yamlRunner, where string) (*apigen.RunnerConfig, error) {
	if yr == nil {
		return nil, nil
	}
	hasOS := yr.OsProcess != nil
	hasSystemd := yr.Systemd != nil
	if hasOS && hasSystemd {
		return nil, invalidConfigErrf("%s.runner: only one of osProcess or systemd may be set", where)
	}
	out := &apigen.RunnerConfig{}
	if hasOS {
		out.OsProcess = &apigen.OsProcessRunnerConfig{
			WorkingDir: yr.OsProcess.WorkingDir,
			RunAs:      yr.OsProcess.RunAs,
			Strategy:   yr.OsProcess.Strategy,
		}
	}
	if hasSystemd {
		if yr.Systemd.Name == "" {
			return nil, invalidConfigErrf("%s.runner.systemd: name is required", where)
		}
		if yr.Systemd.BinPath == "" {
			return nil, invalidConfigErrf("%s.runner.systemd: binPath is required", where)
		}
		out.Systemd = &apigen.SystemdRunnerConfig{
			Name:    yr.Systemd.Name,
			BinPath: yr.Systemd.BinPath,
		}
	}
	return out, nil
}

func invalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	e.InternalErr = fmt.Sprintf(format, args...)
	return e
}
