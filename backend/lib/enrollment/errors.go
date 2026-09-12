package enrollment

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var IdentifierEnrolledErr = apigen.NewApiErr("This machine identifier is already enrolled", "enrollment_identifier_enrolled", http.StatusConflict)

func Rejected(err error) bool {
	var ptr *apigen.ApiErr
	if errors.As(err, &ptr) {
		return ptr.Code == http.StatusConflict
	}
	var val apigen.ApiErr
	return errors.As(err, &val) && val.Code == http.StatusConflict
}
