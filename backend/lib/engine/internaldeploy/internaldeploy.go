package internaldeploy

import (
	"runtime"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	SpaceID       int32 = 0
	Repo                = "github.com/jptrs93/opsagent"
	SelfName            = "opendeploy"
	SelfUnit            = SelfName + ".service"
	ReleaseAsset        = "opendeploy-linux-" + runtime.GOARCH
	NetproxyName        = "opendeploy-net"
	NetproxyImage       = "opendeploy-net"
)

func IsSelfIdentity(spaceID int32, name string) bool {
	return spaceID == SpaceID && name == SelfName
}

func IsNetproxyIdentity(spaceID int32, name string) bool {
	return spaceID == SpaceID && name == NetproxyName
}

func IsInternalIdentity(spaceID int32, name string) bool {
	return IsSelfIdentity(spaceID, name) || IsNetproxyIdentity(spaceID, name)
}

func IsNetproxyConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsNetproxyIdentity(cfg.SpaceID, cfg.Name)
}
