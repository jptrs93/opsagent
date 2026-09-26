package users

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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

func TestMigrateDuplicateCredentialsKeepsTheNewestEntryPerID(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	cred := func(id, data string) *apigen.WebAuthnCredential {
		return &apigen.WebAuthnCredential{ID: []byte(id), Data: []byte(data)}
	}
	Write(s, &apigen.InternalUser{ID: 1, Name: "dup", WebAuthNID: []byte("w1"), Credentials: []*apigen.WebAuthnCredential{
		cred("a", "a1"), cred("b", "b1"), cred("a", "a2"), cred("a", "a3"), cred("b", "b2"),
	}})
	Write(s, &apigen.InternalUser{ID: 2, Name: "clean", WebAuthNID: []byte("w2"), Credentials: []*apigen.WebAuthnCredential{cred("c", "c1")}})
	for range 2 {
		if err := MigrateDuplicateCredentials(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ByID(s.Queries(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Credentials) != 2 || string(got.Credentials[0].Data) != "a3" || string(got.Credentials[1].Data) != "b2" {
		t.Fatalf("credentials after migration = %v", got.Credentials)
	}
	if string(got.WebAuthNID) != "w1" || got.Name != "dup" {
		t.Fatalf("user fields changed: %+v", got)
	}
	clean, err := ByID(s.Queries(), 2)
	if err != nil || len(clean.Credentials) != 1 {
		t.Fatalf("clean user = %+v, %v", clean, err)
	}
}
