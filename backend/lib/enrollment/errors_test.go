package enrollment

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestRejectedMatchesWireDecodedAndWrappedErrors(t *testing.T) {
	decoded, err := apigen.DecodeApiErr(IdentifierEnrolledErr.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.InternalErr != "" {
		t.Fatal("internal code unexpectedly crossed the wire")
	}
	if !Rejected(decoded) {
		t.Fatal("decoded rejection was not recognised")
	}
	if !Rejected(fmt.Errorf("stream: %w", IdentifierEnrolledErr)) {
		t.Fatal("wrapped rejection was not recognised")
	}
	if Rejected(errors.New("connection reset")) {
		t.Fatal("transport error treated as rejection")
	}
	badRequest := apigen.NewApiErr("Secondary certificate request is invalid", "enrollment_invalid_csr", http.StatusBadRequest)
	decoded, err = apigen.DecodeApiErr(badRequest.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if Rejected(decoded) || Rejected(badRequest) {
		t.Fatal("bad request treated as rejection")
	}
}
