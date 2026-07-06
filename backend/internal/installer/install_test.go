package installer

import (
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

func TestRenderEnvTemplateWritesSplitWebSettings(t *testing.T) {
	httpOnly := true
	webListen := ":8080"
	env := string(renderEnvTemplate(installOptions{
		httpOnly:  &httpOnly,
		webListen: &webListen,
	}, nil))

	for _, want := range []string{
		"OPENDEPLOY_INITIAL_WEB_HTTP_ENABLED=true",
		"OPENDEPLOY_INITIAL_WEB_HTTP_LISTEN=:8080",
		"OPENDEPLOY_INITIAL_WEB_HTTPS_ENABLED=false",
		"OPENDEPLOY_INITIAL_WEB_HTTPS_LISTEN=:8080",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q:\n%s", want, env)
		}
	}
	for _, oldKey := range []string{
		"OPENDEPLOY_INITIAL_WEB_HTTP_ONLY=",
		"OPENDEPLOY_INITIAL_WEB_LISTEN=",
	} {
		if strings.Contains(env, oldKey) {
			t.Fatalf("env contains old key %q:\n%s", oldKey, env)
		}
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
	env := string(renderEnvTemplate(opts, nil))
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
