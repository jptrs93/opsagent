package internaldeploy

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	SpaceID       int32 = 0
	Repo                = "github.com/jptrs93/opsagent"
	SelfName            = "opendeploy"
	NetproxyName        = "opendeploy-net"
	NetproxyImage       = "opendeploy-net"
)

func IsSelfIdentifier(cid apigen.DeploymentIdentifier) bool {
	return cid.SpaceID == SpaceID && cid.Name == SelfName
}

func IsNetproxyIdentifier(cid apigen.DeploymentIdentifier) bool {
	return cid.SpaceID == SpaceID && cid.Name == NetproxyName
}

func IsInternalIdentifier(cid apigen.DeploymentIdentifier) bool {
	return IsSelfIdentifier(cid) || IsNetproxyIdentifier(cid)
}

func IsNetproxyConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsNetproxyIdentifier(cfg.ConfigID)
}
