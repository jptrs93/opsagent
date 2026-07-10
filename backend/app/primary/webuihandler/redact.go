package webuihandler

import "github.com/jptrs93/opsagent/backend/apigen"

func redactDeploymentWithStatus(in *apigen.DeploymentWithStatus) *apigen.DeploymentWithStatus {
	if in == nil {
		return nil
	}
	out := *in
	out.Config = *redactDeploymentConfig(&out.Config)
	return &out
}

func redactDeploymentConfig(in *apigen.DeploymentConfig) *apigen.DeploymentConfig {
	if in == nil {
		return nil
	}
	out := *in
	if !out.Spec.Runner.Systemd.IsZero() {
		out.Spec.Runner = apigen.RunnerConfig{}
	}
	return &out
}
