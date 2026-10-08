package webuihandler

import (
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
)

func TestValidateResolvedSettings(t *testing.T) {
	build := func(httpEnabled bool, httpListen string, httpsEnabled bool, httpsListen string) *apigen.ClusterSettings {
		return &apigen.ClusterSettings{
			HttpWeb:  apigen.HttpWebSettings{Enabled: systemconfig.BoolLiteral(httpEnabled), Listen: systemconfig.StringLiteral(httpListen)},
			HttpsWeb: apigen.HttpsWebSettings{Enabled: systemconfig.BoolLiteral(httpsEnabled), Listen: systemconfig.StringLiteral(httpsListen)},
			Cluster:  apigen.ClusterListenSettings{Listen: systemconfig.StringLiteral(":7443"), EnrollmentListen: systemconfig.StringLiteral(":7444")},
		}
	}
	cases := []struct {
		name     string
		settings *apigen.ClusterSettings
		wantErr  string
	}{
		{name: "https only", settings: build(false, "", true, ":8443")},
		{name: "http only", settings: build(true, ":8080", false, "")},
		{name: "both on distinct ports", settings: build(true, ":8080", true, ":8443")},
		{name: "both disabled", settings: build(false, ":8080", false, ":8443"), wantErr: "at least one of"},
		{name: "same listen for both", settings: build(true, ":8443", true, ":8443"), wantErr: "must differ"},
		{name: "same listen after trimming", settings: build(true, " :8443 ", true, ":8443"), wantErr: "must differ"},
		{name: "missing http listen", settings: build(true, "", false, ":8443"), wantErr: "http_web.listen is required"},
		{name: "disabled server ignores its listen", settings: build(false, "", true, ":8443")},
		{name: "listen without a port", settings: build(false, "", true, "127.0.0.1"), wantErr: "https_web.listen must be a listen address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResolvedSettings(tc.settings)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateResolvedSettingsRequiresClusterListeners(t *testing.T) {
	settings := &apigen.ClusterSettings{
		HttpsWeb: apigen.HttpsWebSettings{Enabled: systemconfig.BoolLiteral(true), Listen: systemconfig.StringLiteral(":8443")},
		Cluster:  apigen.ClusterListenSettings{Listen: systemconfig.StringLiteral(":7443")},
	}
	err := validateResolvedSettings(settings)
	if err == nil || !strings.Contains(err.Error(), "cluster.enrollment_listen is required") {
		t.Fatalf("got %v, want the enrollment listen to be required", err)
	}
}
