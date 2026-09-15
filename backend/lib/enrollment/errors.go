package enrollment

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var IdentifierEnrolledErr = apigen.NewApiErr("This machine identifier is already enrolled", "enrollment_identifier_enrolled", http.StatusConflict)

var IdentifierEvictedErr = apigen.NewApiErr("This machine identifier was evicted from the cluster; reinstall the secondary to enroll a new identity", "enrollment_identifier_evicted", http.StatusConflict)

var NodeEvictedErr = apigen.NewApiErr("This node was evicted from the cluster; reinstall the secondary to enroll a new identity", "node_evicted", http.StatusGone)

func Evicted(err error) bool {
	var ptr *apigen.ApiErr
	if errors.As(err, &ptr) {
		return ptr.Code == http.StatusGone
	}
	var val apigen.ApiErr
	return errors.As(err, &val) && val.Code == http.StatusGone
}

func Rejected(err error) bool {
	var ptr *apigen.ApiErr
	if errors.As(err, &ptr) {
		return ptr.Code == http.StatusConflict
	}
	var val apigen.ApiErr
	return errors.As(err, &val) && val.Code == http.StatusConflict
}
