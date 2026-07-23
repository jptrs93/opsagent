package internaldeploy

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	SpaceID       int32 = 0
	Repo                = "github.com/jptrs93/opsagent"
	SelfName            = "opendeploy"
	NetproxyName        = "opendeploy-net"
	NetproxyImage       = "opendeploy-net"
)

func IsSelfIdentity(identity apigen.DeploymentIdentity) bool {
	return identity.SpaceID == SpaceID && identity.Name == SelfName
}

func IsNetproxyIdentity(identity apigen.DeploymentIdentity) bool {
	return identity.SpaceID == SpaceID && identity.Name == NetproxyName
}

func IsInternalIdentity(identity apigen.DeploymentIdentity) bool {
	return IsSelfIdentity(identity) || IsNetproxyIdentity(identity)
}

func IsNetproxyConfig(cfg *apigen.DeploymentConfig2) bool {
	return cfg != nil && IsNetproxyIdentity(cfg.Identity)
}
