package webuihandler

import "github.com/jptrs93/opsagent/backend/apigen"

func redactScheduledInstanceState(in *apigen.ScheduledInstanceState) *apigen.ScheduledInstanceState {
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
	if out.Spec.SystemdSpec != nil {
		systemd := *out.Spec.SystemdSpec
		systemd.Runtime = nil
		out.Spec.SystemdSpec = &systemd
	}
	return &out
}
