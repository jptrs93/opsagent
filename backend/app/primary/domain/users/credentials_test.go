package users

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSetCredentialReplacesTheEntryWithTheSameID(t *testing.T) {
	u := &apigen.InternalUser{}
	SetCredential(u, []byte("a"), []byte("a1"))
	SetCredential(u, []byte("b"), []byte("b1"))
	SetCredential(u, []byte("a"), []byte("a2"))
	if len(u.Credentials) != 2 || string(u.Credentials[0].Data) != "a2" || string(u.Credentials[1].Data) != "b1" {
		t.Fatalf("credentials = %v", u.Credentials)
	}
}
