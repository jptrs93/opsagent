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
		{"self-signed tls defaults to localhost", installOptions{webTLSSelfSigned: &yes}, []string{"localhost"}, "https://localhost"},
		{"own certificate defaults to localhost", installOptions{webTLSCertPEM: ptr("-----BEGIN CERTIFICATE-----")}, []string{"localhost"}, "https://localhost"},
		{"self-signed tls on 8443 with a name", installOptions{webTLSSelfSigned: &yes, webListen: ptr(":8443"), webHosts: ptr("mybox.local")}, []string{"mybox.local"}, "https://mybox.local:8443"},
		{"explicit hosts win", installOptions{httpOnly: &yes, webHosts: ptr("a.local, b.local")}, []string{"a.local", "b.local"}, "http://a.local:8080"},
		{"self-signed false is not local style", installOptions{webTLSSelfSigned: &no}, []string{"opendeploy.example.com"}, "https://opendeploy.example.com"},
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
	if localCAInUse(installOptions{}) || localCAInUse(installOptions{httpOnly: &yes, webTLSSelfSigned: &yes}) {
		t.Fatal("local CA reported in use for an ACME or HTTP-only install")
	}
	pem := "-----BEGIN CERTIFICATE-----"
	if localCAInUse(installOptions{webTLSCertPEM: &pem}) {
		t.Fatal("local CA reported in use when the operator supplied a bundle")
	}
	if !localCAInUse(installOptions{webTLSSelfSigned: &yes}) {
		t.Fatal("local CA not reported for a self-signed install")
	}
	text := caTrustInstructions("/var/lib/opendeploy/web-ca.crt", "https://localhost:8443/v1/tls/ca.crt")
	for _, want := range []string{"/var/lib/opendeploy/web-ca.crt", "https://localhost:8443/v1/tls/ca.crt", "add-trusted-cert", "update-ca-certificates", "certutil"} {
		if !strings.Contains(text, want) {
			t.Fatalf("instructions lack %q:\n%s", want, text)
		}
	}
}

func TestWebTLSFlagsAreMutuallyExclusive(t *testing.T) {
	yes := true
	pem := "-----BEGIN CERTIFICATE-----"
	for _, tc := range []struct {
		name string
		opts installOptions
		ok   bool
	}{
		{"nothing set is acme", installOptions{}, true},
		{"self-signed alone", installOptions{webTLSSelfSigned: &yes}, true},
		{"bundle alone", installOptions{webTLSCertPEM: &pem}, true},
		{"http only alone", installOptions{httpOnly: &yes}, true},
		{"self-signed with bundle", installOptions{webTLSSelfSigned: &yes, webTLSCertPEM: &pem}, false},
		{"http only with self-signed", installOptions{httpOnly: &yes, webTLSSelfSigned: &yes}, false},
		{"http only with bundle", installOptions{httpOnly: &yes, webTLSCertPEM: &pem}, false},
	} {
		if err := tc.opts.validateWebTLS(); (err == nil) != tc.ok {
			t.Fatalf("%s: validateWebTLS() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	if !(installOptions{webTLSSelfSigned: &yes}).selfManagedTLS() || !(installOptions{webTLSCertPEM: &pem}).selfManagedTLS() || (installOptions{}).selfManagedTLS() {
		t.Fatal("selfManagedTLS must be true for either certificate option and false for acme")
	}
}

func ptr(s string) *string { return &s }
