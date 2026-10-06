package internaldeploy

import (
	"runtime"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	SpaceID       uint64 = 0
	Repo                 = "github.com/jptrs93/opsagent"
	SelfName             = "opendeploy"
	SelfImage            = "opendeploy"
	SelfUnit             = SelfName + ".service"
	ReleaseAsset         = "opendeploy-linux-" + runtime.GOARCH
	NetproxyName         = "opendeploy-net"
	NetproxyImage        = "opendeploy-net"
)

func IsSelfIdentity(spaceID uint64, name string) bool {
	return spaceID == SpaceID && name == SelfName
}

func IsNetproxyIdentity(spaceID uint64, name string) bool {
	return spaceID == SpaceID && name == NetproxyName
}

func IsInternalIdentity(spaceID uint64, name string) bool {
	return IsSelfIdentity(spaceID, name) || IsNetproxyIdentity(spaceID, name)
}

func IsNetproxyConfig(cfg *apigen.DeploymentRecord) bool {
	return cfg != nil && IsNetproxyIdentity(cfg.Deployment.SpaceID, cfg.Deployment.Name)
}
