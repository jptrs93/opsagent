package certu

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const WebUILocalCAFileName = "web-ca.crt"

// WebUILocalCAPath is where the CA certificate is exported for operators.
func WebUILocalCAPath(dataDir string) string {
	return filepath.Join(dataDir, WebUILocalCAFileName)
}

// WriteWebUILocalCAFile exports the CA certificate world-readable; it holds
// no secret, and the whole point is for other tools and people to read it.
func WriteWebUILocalCAFile(dataDir string, caCertPEM []byte) error {
	path := WebUILocalCAPath(dataDir)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, caCertPEM) {
		return nil
	}
	return os.WriteFile(path, caCertPEM, 0o644)
}

// WebUITLSNames are the names the Web UI certificate must cover: the
// configured hostnames plus the listen host. Loopback names are added by the
// signer.
func WebUITLSNames(acmeHosts, listen string) []string {
	var names []string
	for _, name := range strings.Split(acmeHosts, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	host = strings.Trim(host, "[]")
	if err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		names = append(names, host)
	}
	return names
}

func SecondaryTLSPaths(tlsDir string) (caPath, certPath, keyPath string) {
	return filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "node.crt"), filepath.Join(tlsDir, "node.key")
}

func SecondaryTLSMaterialExists(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
