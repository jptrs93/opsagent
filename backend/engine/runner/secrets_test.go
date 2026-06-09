package runner

import "testing"

type fakeResolver map[string]string

func (f fakeResolver) Resolve(name string) (string, bool)       { v, ok := f[name]; return v, ok }
func (f fakeResolver) ResolveConfig(name string) (string, bool) { v, ok := f[name]; return v, ok }

func withResolvers(secrets SecretResolver, configs ConfigResolver, fn func()) {
	prevSecrets := Secrets
	prevConfigs := Configs
	Secrets = secrets
	Configs = configs
	defer func() { Secrets = prevSecrets; Configs = prevConfigs }()
	fn()
}

func TestResolveEnv(t *testing.T) {
	withResolvers(fakeResolver{"db.pass": "s3cret", "tok": "abc"}, fakeResolver{"host": "db.local"}, func() {
		in := []string{
			"PLAIN=value",
			"DB_PASS=${s:db.pass}",
			"URL=postgres://u:${s:db.pass}@${c:host}/db",
			"MIX=${s:tok}-${s:db.pass}-${c:host}",
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
			"URL=postgres://u:s3cret@db.local/db",
			"MIX=abc-s3cret-db.local",
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
	withResolvers(fakeResolver{}, fakeResolver{}, func() {
		if _, err := resolveEnv([]string{"X=${s:missing}"}); err == nil {
			t.Fatal("expected error for unknown secret")
		}
	})
}

func TestResolveEnvUnknownConfigFailsClosed(t *testing.T) {
	withResolvers(fakeResolver{}, fakeResolver{}, func() {
		if _, err := resolveEnv([]string{"X=${c:missing}"}); err == nil {
			t.Fatal("expected error for unknown config")
		}
	})
}

func TestResolveEnvNoResolverFailsClosed(t *testing.T) {
	withResolvers(nil, nil, func() {
		if _, err := resolveEnv([]string{"X=${s:anything}"}); err == nil {
			t.Fatal("expected error when no resolver is set")
		}
		if _, err := resolveEnv([]string{"X=${c:anything}"}); err == nil {
			t.Fatal("expected error when no config resolver is set")
		}
		// ...but values without placeholders still pass through.
		out, err := resolveEnv([]string{"X=plain"})
		if err != nil || out[0] != "X=plain" {
			t.Fatalf("plain passthrough failed: %v %q", err, out)
		}
	})
}

func TestExpandSecretsUnterminated(t *testing.T) {
	withResolvers(fakeResolver{}, fakeResolver{}, func() {
		if _, err := expandRefs("${unterminated"); err == nil {
			t.Fatal("expected error for unterminated reference")
		}
	})
}

func TestExpandRefsRejectsLegacySyntax(t *testing.T) {
	withResolvers(fakeResolver{"db.pass": "s3cret"}, fakeResolver{}, func() {
		if _, err := resolveEnv([]string{"X=${db.pass}"}); err == nil {
			t.Fatal("expected error for legacy secret reference")
		}
	})
}
