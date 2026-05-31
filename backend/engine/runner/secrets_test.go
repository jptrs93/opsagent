package runner

import "testing"

type fakeResolver map[string]string

func (f fakeResolver) Resolve(name string) (string, bool) { v, ok := f[name]; return v, ok }

func withResolver(r SecretResolver, fn func()) {
	prev := Secrets
	Secrets = r
	defer func() { Secrets = prev }()
	fn()
}

func TestResolveEnv(t *testing.T) {
	withResolver(fakeResolver{"db.pass": "s3cret", "tok": "abc"}, func() {
		in := []string{
			"PLAIN=value",
			"DB_PASS=${db.pass}",
			"URL=postgres://u:${db.pass}@host/db",
			"MIX=${tok}-${db.pass}",
			"LITERAL=cost is $$5",
			"NOEQUALSIGN",
		}
		out, err := resolveEnv(in)
		if err != nil {
			t.Fatalf("resolveEnv: %v", err)
		}
		want := []string{
			"PLAIN=value",
			"DB_PASS=s3cret",
			"URL=postgres://u:s3cret@host/db",
			"MIX=abc-s3cret",
			"LITERAL=cost is $5",
			"NOEQUALSIGN",
		}
		for i := range want {
			if out[i] != want[i] {
				t.Errorf("out[%d] = %q; want %q", i, out[i], want[i])
			}
		}
	})
}

func TestResolveEnvUnknownSecretFailsClosed(t *testing.T) {
	withResolver(fakeResolver{}, func() {
		if _, err := resolveEnv([]string{"X=${missing}"}); err == nil {
			t.Fatal("expected error for unknown secret")
		}
	})
}

func TestResolveEnvNoResolverFailsClosed(t *testing.T) {
	withResolver(nil, func() {
		if _, err := resolveEnv([]string{"X=${anything}"}); err == nil {
			t.Fatal("expected error when no resolver is set")
		}
		// ...but values without placeholders still pass through.
		out, err := resolveEnv([]string{"X=plain"})
		if err != nil || out[0] != "X=plain" {
			t.Fatalf("plain passthrough failed: %v %q", err, out)
		}
	})
}

func TestExpandSecretsUnterminated(t *testing.T) {
	withResolver(fakeResolver{}, func() {
		if _, err := expandSecrets("${unterminated"); err == nil {
			t.Fatal("expected error for unterminated reference")
		}
	})
}
