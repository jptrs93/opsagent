package runner

import (
	"fmt"
	"strings"
)

// SecretResolver resolves a secret name to its plaintext value. It is set at
// startup (on the primary) to the secrets manager.
type SecretResolver interface {
	Resolve(name string) (value string, ok bool)
}

// ConfigResolver resolves a plain user config by name. It is intentionally a
// separate interface because config values are not encrypted at rest.
type ConfigResolver interface {
	ResolveConfig(name string) (value string, ok bool)
}

// Secrets and Configs are process-wide resolvers used to expand ${s:name} and
// ${c:name} placeholders in deployment env values at spawn time. Resolution
// happens at spawn (not at config time) so referenced values are picked up on
// the next (re)start.
var Secrets SecretResolver
var Configs ConfigResolver

// resolveEnv expands ${s:name} secret and ${c:name} config references in each "KEY=VALUE" entry's
// value. A value with no placeholder is passed through untouched (so
// deployments without references need no resolver). Any unresolved reference
// returns an error, which the runner turns into a failed spawn.
func resolveEnv(env []string) ([]string, error) {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		expanded, err := expandRefs(val)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
		out = append(out, key+"="+expanded)
	}
	return out, nil
}

// expandRefs replaces ${s:name} with a resolved secret and ${c:name} with a
// plain config value. "$$" is an escape for a literal "$". Error messages
// reference the non-sensitive plaintext key, never the resolved value.
func expandRefs(s string) (string, error) {
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
					return "", fmt.Errorf("unterminated reference")
				}
				ref := strings.TrimSpace(s[i+2 : i+2+end])
				val, err := resolveRef(ref)
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

func resolveRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty reference ${}")
	}
	prefix, name, ok := strings.Cut(ref, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("reference ${%s} must use ${s:name} or ${c:name}", ref)
	}
	name = strings.TrimSpace(name)
	switch strings.TrimSpace(prefix) {
	case "s":
		return resolveSecretRef(name)
	case "c":
		return resolveConfigRef(name)
	default:
		return "", fmt.Errorf("unknown reference type ${%s}; use ${s:name} or ${c:name}", ref)
	}
}

func resolveSecretRef(name string) (string, error) {
	if Secrets == nil {
		return "", fmt.Errorf("unknown secret ${s:%s}: no secrets store on this node", name)
	}
	val, ok := Secrets.Resolve(name)
	if !ok {
		return "", fmt.Errorf("unknown secret ${s:%s}", name)
	}
	return val, nil
}

func resolveConfigRef(name string) (string, error) {
	if Configs == nil {
		return "", fmt.Errorf("unknown config ${c:%s}: no config store on this node", name)
	}
	val, ok := Configs.ResolveConfig(name)
	if !ok {
		return "", fmt.Errorf("unknown config ${c:%s}", name)
	}
	return val, nil
}
