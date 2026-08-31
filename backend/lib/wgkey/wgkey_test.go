package wgkey

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrGenerateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	if first.PublicBase64() == "" || first.Public != first.Private.PublicKey() {
		t.Fatalf("generated key is inconsistent: %+v", first)
	}
	second, err := LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate reload: %v", err)
	}
	if second.Private != first.Private {
		t.Fatalf("reload minted a new key")
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoadOrGenerateRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("not-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerate(dir); err == nil {
		t.Fatalf("corrupt key file did not error")
	}
}

func TestValidatePublic(t *testing.T) {
	if got, err := ValidatePublic("  "); err != nil || got != "" {
		t.Fatalf("blank key: got %q err %v, want empty and no error", got, err)
	}
	key, err := LoadOrGenerate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ValidatePublic(" " + key.PublicBase64() + "\n"); err != nil || got != key.PublicBase64() {
		t.Fatalf("valid key: got %q err %v", got, err)
	}
	for _, invalid := range []string{"zzz", "QUFB", key.PublicBase64()[:42]} {
		if _, err := ValidatePublic(invalid); err == nil {
			t.Fatalf("invalid key %q accepted", invalid)
		}
	}
}
