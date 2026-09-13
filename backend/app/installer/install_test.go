package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/goutil/authu"
)

func TestGenerateBootstrapCredentialsUsesHighEntropyPassword(t *testing.T) {
	bootstrap, err := generateBootstrapCredentials(installOptions{role: "primary"})
	if err != nil {
		t.Fatalf("generateBootstrapCredentials: %v", err)
	}
	if bootstrap == nil {
		t.Fatal("bootstrap credentials were nil")
	}
	if !strings.HasPrefix(bootstrap.password, "opendeploy-") {
		t.Fatalf("password = %q, want opendeploy- prefix", bootstrap.password)
	}
	if len(bootstrap.password) < len("opendeploy-")+40 {
		t.Fatalf("password length = %d, want at least %d", len(bootstrap.password), len("opendeploy-")+40)
	}
	ok, err := authu.VerifyPassword(bootstrap.password, bootstrap.hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("bootstrap password did not verify against hash")
	}
}

func TestGenerateBootstrapCredentialsSkippedForSecondary(t *testing.T) {
	bootstrap, err := generateBootstrapCredentials(installOptions{role: "secondary"})
	if err != nil {
		t.Fatalf("generateBootstrapCredentials: %v", err)
	}
	if bootstrap != nil {
		t.Fatal("secondary install should not generate bootstrap credentials")
	}
}

func TestRenderEnvTemplateDoesNotPersistInitialSettings(t *testing.T) {
	httpOnly := true
	webListen := ":8080"
	env := string(renderEnvTemplate(installOptions{
		httpOnly:  &httpOnly,
		webListen: &webListen,
	}))

	if strings.Contains(env, "OPENDEPLOY_INITIAL_") {
		t.Fatalf("env contains initial bootstrap settings:\n%s", env)
	}
}

func TestRenderEnvTemplateDoesNotWriteInitialWebTLS(t *testing.T) {
	certPEM := "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\ndef\n-----END PRIVATE KEY-----\n"
	env := string(renderEnvTemplate(installOptions{webTLSCertPEM: &certPEM}))

	if strings.Contains(env, "TLS_CERT") || strings.Contains(env, "abc") || strings.Contains(env, "def") {
		t.Fatalf("env contains TLS PEM material:\n%s", env)
	}
}

func TestStripInitialEnvValuesPreservesRuntimeSettings(t *testing.T) {
	env := stripInitialEnvValues([]byte("OPENDEPLOY_INITIAL_MASTER_PASSWORD_HASH=hash\nOPENDEPLOY_INITIAL_WEB_HTTPS_ENABLED=true\nOPENDEPLOY_PRIMARY_NAME=primary\n"))
	if strings.Contains(string(env), "OPENDEPLOY_INITIAL_") {
		t.Fatalf("initial values remain:\n%s", env)
	}
	if !strings.Contains(string(env), "OPENDEPLOY_PRIMARY_NAME=primary") {
		t.Fatalf("runtime setting was removed:\n%s", env)
	}
}

func TestParseInstallPrimaryRejectsEmptyTLSPEMFile(t *testing.T) {
	path := t.TempDir() + "/empty.pem"
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, _, err := parseInstallPrimary([]string{"--web-tls-cert-pem-file", path})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("parseInstallPrimary err = %v, want empty PEM error", err)
	}
}

func TestParseInstallPrimaryRejectsInvalidTLSPEMFile(t *testing.T) {
	path := t.TempDir() + "/invalid.pem"
	if err := os.WriteFile(path, []byte("not a cert"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, _, err := parseInstallPrimary([]string{"--web-tls-cert-pem-file", path})
	if err == nil || !strings.Contains(err.Error(), "certificate chain and private key") {
		t.Fatalf("parseInstallPrimary err = %v, want invalid PEM error", err)
	}
}

func TestParseInstallSecondaryRequiresEnrollmentFingerprint(t *testing.T) {
	_, _, err := parseInstallSecondary([]string{"--cluster-addr", "primary:9443", "--enrollment-addr", "primary:9444"})
	if err == nil || !strings.Contains(err.Error(), "--enrollment-fingerprint") {
		t.Fatalf("parseInstallSecondary err = %v, want enrollment fingerprint error", err)
	}
}

func TestParseInstallSecondaryStoresEnrollmentFingerprint(t *testing.T) {
	_, opts, err := parseInstallSecondary([]string{
		"--cluster-addr", "primary:9443",
		"--enrollment-addr", "primary:9444",
		"--enrollment-fingerprint", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatalf("parseInstallSecondary: %v", err)
	}
	if opts.enrollmentFingerprint == nil || *opts.enrollmentFingerprint == "" {
		t.Fatal("enrollment fingerprint was not stored")
	}
	env := string(renderEnvTemplate(opts))
	if !strings.Contains(env, "OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("env missing enrollment fingerprint:\n%s", env)
	}
}

func TestParseInstallStoresOptionalUnderlayAddress(t *testing.T) {
	_, opts, err := parseInstallSecondary([]string{
		"--cluster-addr", "primary:9443",
		"--enrollment-addr", "primary:9444",
		"--enrollment-fingerprint", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--underlay-address", "10.0.0.2",
	})
	if err != nil {
		t.Fatalf("parseInstallSecondary: %v", err)
	}
	if opts.underlayAddress == nil || *opts.underlayAddress != "10.0.0.2" {
		t.Fatalf("underlay address = %v, want 10.0.0.2", opts.underlayAddress)
	}
	env := string(renderEnvTemplate(opts))
	if !strings.Contains(env, "OPENDEPLOY_UNDERLAY_ADDRESS=10.0.0.2") {
		t.Fatalf("env missing underlay address:\n%s", env)
	}
}

func TestRenderOpenDeployUnitUsesRestartAlways(t *testing.T) {
	unit := renderOpenDeployUnit(installOptions{role: "primary"})
	if !strings.Contains(string(unit), "Restart=always") {
		t.Fatalf("unit missing Restart=always:\n%s", unit)
	}
}

func TestRoleFromUnitReadsInstalledRole(t *testing.T) {
	for _, role := range []string{"primary", "secondary"} {
		got, err := roleFromUnit(renderOpenDeployUnit(installOptions{role: role}))
		if err != nil {
			t.Fatalf("roleFromUnit(%s): %v", role, err)
		}
		if got != role {
			t.Fatalf("roleFromUnit(%s) = %q", role, got)
		}
	}
}

func TestRoleFromUnitRejectsUnexpectedUnits(t *testing.T) {
	_, err := roleFromUnit([]byte("[Service]\nExecStart=/usr/local/bin/opendeploy primary\n"))
	if err == nil || !strings.Contains(err.Error(), "unexpected ExecStart") {
		t.Fatalf("foreign ExecStart err = %v", err)
	}
	_, err = roleFromUnit([]byte("[Service]\nExecStart=/var/lib/opendeploy/bin/opendeploy dataplane\n"))
	if err == nil || !strings.Contains(err.Error(), "unexpected ExecStart") {
		t.Fatalf("non-role ExecStart err = %v", err)
	}
	_, err = roleFromUnit([]byte("[Service]\nUser=opendeploy\n"))
	if err == nil || !strings.Contains(err.Error(), "no ExecStart") {
		t.Fatalf("missing ExecStart err = %v", err)
	}
}

func TestParseUpgradeRejectsRoleArgument(t *testing.T) {
	_, err := parseUpgrade([]string{"secondary"})
	if err == nil || !strings.Contains(err.Error(), "takes no role") {
		t.Fatalf("parseUpgrade err = %v, want role rejection", err)
	}
}

func TestParseUpgradeVersion(t *testing.T) {
	version, err := parseUpgrade(nil)
	if err != nil || version != "" {
		t.Fatalf("parseUpgrade() = %q, %v", version, err)
	}
	version, err = parseUpgrade([]string{"--version", "v1.2.3"})
	if err != nil || version != "v1.2.3" {
		t.Fatalf("parseUpgrade(--version) = %q, %v", version, err)
	}
}

func TestUpgradedEnvIsStableAcrossUpgrades(t *testing.T) {
	opts := installOptions{role: "primary"}
	first := upgradedEnv([]byte("OPENDEPLOY_INITIAL_MASTER_PASSWORD_HASH=hash\nOPENDEPLOY_PRIMARY_NAME=primary\nOPENDEPLOY_GITHUB_TOKEN='a b'\n\n"), opts)
	if strings.Contains(string(first), "OPENDEPLOY_INITIAL_") {
		t.Fatalf("initial values remain:\n%s", first)
	}
	second := upgradedEnv(first, opts)
	if !bytes.Equal(first, second) {
		t.Fatalf("second pass changed the env:\n%s\n---\n%s", first, second)
	}
}

func TestInstallBinaryReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "bin", "opendeploy")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fileBytesEqual(src, dst) {
		t.Fatal("src and dst compared equal before install")
	}
	if err := installBinary(src, dst, 0o755, noChown); err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	if !fileBytesEqual(src, dst) {
		t.Fatal("dst was not replaced")
	}
	if pathExists(dst + ".tmp") {
		t.Fatal("temp file left behind")
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("dst mode = %o, want 755", st.Mode().Perm())
	}
}
