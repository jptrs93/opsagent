package installer

import (
	"os"
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

func TestRenderOpenDeployUnitUsesRestartAlways(t *testing.T) {
	unit := renderOpenDeployUnit(installOptions{role: "primary"})
	if !strings.Contains(string(unit), "Restart=always") {
		t.Fatalf("unit missing Restart=always:\n%s", unit)
	}
}
