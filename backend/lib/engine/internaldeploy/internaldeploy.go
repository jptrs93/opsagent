package internaldeploy

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	SpaceID        int32 = 0
	Repo                 = "github.com/jptrs93/opsagent"
	SelfName             = "opendeploy"
	DataplaneName        = "opendeploy-net"
	DataplaneImage       = "opendeploy-net"
)

func IsSelfIdentifier(cid apigen.DeploymentIdentifier) bool {
	return cid.SpaceID == SpaceID && cid.Name == SelfName
}

func IsDataplaneIdentifier(cid apigen.DeploymentIdentifier) bool {
	return cid.SpaceID == SpaceID && cid.Name == DataplaneName
}

func IsInternalIdentifier(cid apigen.DeploymentIdentifier) bool {
	return IsSelfIdentifier(cid) || IsDataplaneIdentifier(cid)
}

func IsDataplaneConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsDataplaneIdentifier(cfg.ConfigID)
}
