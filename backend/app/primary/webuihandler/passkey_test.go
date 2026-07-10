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
		Config: &apigen.Settings{
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
