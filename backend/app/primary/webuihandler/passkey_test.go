package webuihandler

import (
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
)

func TestPasskeyOriginsIncludesExtraOrigins(t *testing.T) {
	orig := ainit.StaticConfig
	defer func() { ainit.StaticConfig = orig }()
	ainit.StaticConfig.PasskeyExtraOrigins = []string{"https://primary.opendeploy.test:8443", ""}

	h := &Handler{
		ConfigService: &config.Service{},
		Config: &apigen.ClusterSettings{
			HttpWeb: apigen.HttpWebSettings{Enabled: apigen.BoolSetting{Value: false}},
			HttpsWeb: apigen.HttpsWebSettings{
				Enabled:   apigen.BoolSetting{Value: true},
				AcmeHosts: apigen.StringSetting{Value: "primary.opendeploy.test"},
			},
		},
	}

	got, err := h.passkeyOrigins()
	if err != nil {
		t.Fatalf("passkeyOrigins err = %v", err)
	}
	want := []string{"https://primary.opendeploy.test", "https://primary.opendeploy.test:8443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("passkeyOrigins = %#v, want %#v", got, want)
	}
}

func TestPasskeyRPAndOriginsFollowListenPortsAndSchemes(t *testing.T) {
	orig := ainit.StaticConfig
	defer func() { ainit.StaticConfig = orig }()
	ainit.StaticConfig.PasskeyExtraOrigins = nil

	cases := []struct {
		name        string
		cfg         apigen.ClusterSettings
		wantRPID    string
		wantOrigins []string
	}{
		{
			name: "http only on a non-default port defaults to localhost",
			cfg: apigen.ClusterSettings{
				HttpWeb:  apigen.HttpWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: "127.0.0.1:9090"}},
				HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: false}},
			},
			wantRPID:    "localhost",
			wantOrigins: []string{"http://localhost:9090", "http://localhost:5173"},
		},
		{
			name: "http only keeps the localhost RP id even with a placeholder hostname",
			cfg: apigen.ClusterSettings{
				HttpWeb:  apigen.HttpWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: ":8080"}},
				HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: false}, AcmeHosts: apigen.StringSetting{Value: "opendeploy.example.com"}},
			},
			wantRPID:    "localhost",
			wantOrigins: []string{"http://opendeploy.example.com:8080", "http://localhost:8080", "http://localhost:5173"},
		},
		{
			name: "self-managed https on 8443 with a hostname",
			cfg: apigen.ClusterSettings{
				HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: ":8443"}, AcmeHosts: apigen.StringSetting{Value: "opendeploy.local, localhost"}},
			},
			wantRPID:    "opendeploy.local",
			wantOrigins: []string{"https://opendeploy.local:8443", "https://localhost:8443"},
		},
		{
			name: "an address-only host list falls back to localhost as RP id",
			cfg: apigen.ClusterSettings{
				HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: ":443"}, AcmeHosts: apigen.StringSetting{Value: "192.168.1.20"}},
			},
			wantRPID:    "localhost",
			wantOrigins: []string{"https://192.168.1.20"},
		},
		{
			name: "both listeners enabled yields both schemes per host",
			cfg: apigen.ClusterSettings{
				HttpWeb:  apigen.HttpWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: ":8080"}},
				HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: ":443"}, AcmeHosts: apigen.StringSetting{Value: "example.test"}},
			},
			wantRPID:    "example.test",
			wantOrigins: []string{"https://example.test", "http://example.test:8080"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			h := &Handler{ConfigService: &config.Service{}, Config: &cfg}
			rpID, err := h.passkeyRPID()
			if err != nil || rpID != tc.wantRPID {
				t.Fatalf("passkeyRPID = %q, %v; want %q", rpID, err, tc.wantRPID)
			}
			got, err := h.passkeyOrigins()
			if err != nil || !reflect.DeepEqual(got, tc.wantOrigins) {
				t.Fatalf("passkeyOrigins = %#v, %v; want %#v", got, err, tc.wantOrigins)
			}
		})
	}
}
