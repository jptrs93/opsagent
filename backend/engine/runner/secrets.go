package runner

import (
	"fmt"
	"strings"
)

// SecretResolver resolves a secret name to its plaintext value. It is set at
// startup (on the primary) to the secrets manager. When nil — e.g. on a
// secondary in this phase, or before init — any env value referencing ${name}
// fails to resolve and the spawn fails closed.
type SecretResolver interface {
	Resolve(name string) (value string, ok bool)
}

// Secrets is the process-wide resolver used to expand ${name} placeholders in
// deployment env values at spawn time. Resolution happens at spawn (not at
// config time) so values are never persisted, never replicated, and never
// logged (spawnDaemon logs env keys only), and so a rotated secret is picked up
// on the next (re)start.
var Secrets SecretResolver

// resolveEnv expands ${name} secret references in each "KEY=VALUE" entry's
// value. A value with no placeholder is passed through untouched (so
// deployments without secrets need no resolver). Any unresolved reference
// returns an error, which the runner turns into a failed spawn.
func resolveEnv(env []string) ([]string, error) {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		expanded, err := expandSecrets(val)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
		out = append(out, key+"="+expanded)
	}
	return out, nil
}

// expandSecrets replaces ${name} with the resolved secret value. "$$" is an
// escape for a literal "$". Error messages reference the secret name (a
// non-sensitive plaintext key), never the value.
func expandSecrets(s string) (string, error) {
	if !strings.Contains(s, "${") && !strings.Contains(s, "$$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' && i+1 < len(s) {
			switch s[i+1] {
			case '$': // literal "$"
				b.WriteByte('$')
				i += 2
				continue
			case '{':
				end := strings.IndexByte(s[i+2:], '}')
				if end < 0 {
					return "", fmt.Errorf("unterminated secret reference")
				}
				name := strings.TrimSpace(s[i+2 : i+2+end])
				val, err := resolveSecretRef(name)
				if err != nil {
					return "", err
				}
				b.WriteString(val)
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

func resolveSecretRef(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty secret reference ${}")
	}
	if Secrets == nil {
		return "", fmt.Errorf("unknown secret ${%s}: no secrets store on this node", name)
	}
	val, ok := Secrets.Resolve(name)
	if !ok {
		return "", fmt.Errorf("unknown secret ${%s}", name)
	}
	return val, nil
}
