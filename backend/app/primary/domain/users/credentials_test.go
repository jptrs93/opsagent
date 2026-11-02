package users

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSetCredentialReplacesTheEntryWithTheSameID(t *testing.T) {
	u := &apigen.User{}
	t0 := time.Unix(1_700_000_000, 0)
	SetCredential(u, []byte("a"), []byte("a1"), t0)
	SetCredential(u, []byte("b"), []byte("b1"), t0.Add(time.Second))
	SetCredential(u, []byte("a"), []byte("a2"), t0.Add(2*time.Second))
	creds := u.Authentication.Credentials
	if len(creds) != 2 || string(creds[0].Data) != "a2" || string(creds[1].Data) != "b1" {
		t.Fatalf("credentials = %v", creds)
	}
	if !creds[0].CreatedAt.Equal(t0) || !creds[0].LastUsedAt.Present || !creds[0].LastUsedAt.Value.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("replaced credential keeps its creation time and takes a last-used stamp: %+v", creds[0])
	}
	if creds[1].LastUsedAt.Present || creds[0].Label != "Passkey" || creds[1].Label != "Passkey 2" {
		t.Fatalf("new credentials carry a default label and no last-used stamp: %+v", creds)
	}
	if !RemoveCredential(u, []byte("a")) || len(u.Authentication.Credentials) != 1 || RemoveCredential(u, []byte("a")) {
		t.Fatalf("remove = %+v", u.Authentication.Credentials)
	}
}
