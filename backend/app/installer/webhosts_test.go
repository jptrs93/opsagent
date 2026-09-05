package installer

import (
	"reflect"
	"strings"
	"testing"
)

func TestWebHostsDefaultToLocalhostForLocalStyleInstalls(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		opts installOptions
		want []string
		url  string
	}{
		{"acme install keeps the placeholder", installOptions{}, []string{"opendeploy.example.com"}, "https://opendeploy.example.com"},
		{"http only defaults to localhost", installOptions{httpOnly: &yes}, []string{"localhost"}, "http://localhost:8080"},
		{"self-managed tls defaults to localhost", installOptions{webTLSSelfManaged: &yes}, []string{"localhost"}, "https://localhost"},
		{"self-managed tls on 8443 with a name", installOptions{webTLSSelfManaged: &yes, webListen: ptr(":8443"), acmeHosts: ptr("mybox.local")}, []string{"mybox.local"}, "https://mybox.local:8443"},
		{"explicit hosts win", installOptions{httpOnly: &yes, acmeHosts: ptr("a.local, b.local")}, []string{"a.local", "b.local"}, "http://a.local:8080"},
		{"self-managed false is not local style", installOptions{webTLSSelfManaged: &no}, []string{"opendeploy.example.com"}, "https://opendeploy.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := webHosts(tc.opts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("webHosts = %v, want %v", got, tc.want)
			}
			if got := webUIAddrs(tc.opts)[0]; got != tc.url {
				t.Fatalf("webUIAddrs[0] = %q, want %q", got, tc.url)
			}
		})
	}
}

func TestLocalCAInUseAndInstructions(t *testing.T) {
	yes := true
	if localCAInUse(installOptions{}) || localCAInUse(installOptions{httpOnly: &yes, webTLSSelfManaged: &yes}) {
		t.Fatal("local CA reported in use for an ACME or HTTP-only install")
	}
	pem := "-----BEGIN CERTIFICATE-----"
	if localCAInUse(installOptions{webTLSSelfManaged: &yes, webTLSCertPEM: &pem}) {
		t.Fatal("local CA reported in use when the operator supplied a bundle")
	}
	if !localCAInUse(installOptions{webTLSSelfManaged: &yes}) {
		t.Fatal("local CA not reported for a self-managed install without a bundle")
	}
	text := caTrustInstructions("/var/lib/opendeploy/web-ca.crt", "https://localhost:8443/v1/tls/ca.crt")
	for _, want := range []string{"/var/lib/opendeploy/web-ca.crt", "https://localhost:8443/v1/tls/ca.crt", "add-trusted-cert", "update-ca-certificates", "certutil"} {
		if !strings.Contains(text, want) {
			t.Fatalf("instructions lack %q:\n%s", want, text)
		}
	}
}

func ptr(s string) *string { return &s }
