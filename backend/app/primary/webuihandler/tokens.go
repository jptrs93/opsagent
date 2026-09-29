package webuihandler

import (
	"crypto/sha256"
	"strings"

	"github.com/jptrs93/goutil/authu"
)

// A bearer token is "<kind><session id>.<secret>": the kind picks the table,
// the id is the indexed lookup, and only sha256(token) is stored, so the row
// binds to exactly one token and a copy of the database holds no credential.
const (
	userTokenKind  = "u_"
	agentTokenKind = "a_"
)

// tokenDisplayPrefixLen is how much of a token is kept in the clear so an
// operator can tell two sessions apart in the list. Short enough to be useless
// on its own.
const tokenDisplayPrefixLen = 12

func mintToken(kind, sessionID string) (string, error) {
	secret, err := authu.GenerateRandomToken(32)
	if err != nil {
		return "", err
	}
	return kind + sessionID + "." + secret, nil
}

func splitToken(token string) (kind, sessionID string, ok bool) {
	for _, k := range []string{userTokenKind, agentTokenKind} {
		rest, found := strings.CutPrefix(token, k)
		if !found {
			continue
		}
		id, secret, found := strings.Cut(rest, ".")
		if !found || id == "" || secret == "" {
			return "", "", false
		}
		return k, id, true
	}
	return "", "", false
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func tokenDisplayPrefix(token string) string {
	if len(token) <= tokenDisplayPrefixLen {
		return token
	}
	return token[:tokenDisplayPrefixLen]
}
