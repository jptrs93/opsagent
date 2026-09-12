package certu

import (
	"encoding/hex"
	"testing"
)

func TestSecondaryIdentityIsKeyDerivedAndVerifiable(t *testing.T) {
	keyPEM, err := GenerateSecondaryKey()
	if err != nil {
		t.Fatal(err)
	}
	identifier, csrPEM, err := SecondaryIdentity(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(identifier); err != nil || len(raw) != 32 || len(identifier) != 64 {
		t.Fatalf("identifier %q is not 64 lowercase hex characters", identifier)
	}
	againID, againCSR, err := SecondaryIdentity(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if againID != identifier {
		t.Fatalf("identifier changed for the same key: %s vs %s", identifier, againID)
	}
	if err := VerifySecondaryCertificateRequest(csrPEM, identifier); err != nil {
		t.Fatal(err)
	}
	if err := VerifySecondaryCertificateRequest(againCSR, identifier); err != nil {
		t.Fatal(err)
	}
	if err := VerifySecondaryCertificateRequest(csrPEM, "0"+identifier[1:]); err == nil {
		t.Fatal("CSR verified against a different identifier")
	}
	foreign, _, err := GenerateSecondaryCertificateRequest(identifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecondaryCertificateRequest(foreign, identifier); err == nil {
		t.Fatal("CSR from another key verified for this identifier")
	}
	legacy, _, err := GenerateSecondaryCertificateRequest("8c3f0d7e-4d2c-4c1b-9a1e-0c9f6a2b7d10")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecondaryCertificateRequest(legacy, "8c3f0d7e-4d2c-4c1b-9a1e-0c9f6a2b7d10"); err == nil {
		t.Fatal("UUID identifier verified")
	}
	if err := VerifySecondaryCertificateRequest([]byte("not a csr"), identifier); err == nil {
		t.Fatal("malformed CSR verified")
	}
}
