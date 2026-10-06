package users

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSetCredentialReplacesTheEntryWithTheSameID(t *testing.T) {
	u := &apigen.User{}
	SetCredential(u, []byte("a"), []byte("a1"))
	SetCredential(u, []byte("b"), []byte("b1"))
	SetCredential(u, []byte("a"), []byte("a2"))
	creds := u.Authentication.Credentials
	if len(creds) != 2 || string(creds[0].Data) != "a2" || string(creds[1].Data) != "b1" {
		t.Fatalf("credentials = %v", creds)
	}
}
