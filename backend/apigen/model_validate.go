package apigen

import (
	"net/http"
)

func (req *DeploymentUpdateRequestV2) Validate() error {
	if req.DeploymentID == 0 {
		return NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
	}
	kinds := 0
	for _, set := range []bool{req.VersionOnlyUpdate != nil, req.RunningOnlyUpdate != nil, req.SpecUpdate != nil, req.AssignedSpaceUpdate != nil} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return NewApiErr("exactly one update kind must be set", "invalid_config", http.StatusBadRequest)

	}
	return nil
}
