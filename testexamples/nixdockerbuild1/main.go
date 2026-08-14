package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_MESSAGE=%s\n", os.Getenv("OPENDEPLOY_E2E_MESSAGE"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_COLOR=%s\n", os.Getenv("OPENDEPLOY_E2E_COLOR"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_CONFIG=%s\n", os.Getenv("OPENDEPLOY_E2E_CONFIG"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_SECRET=%s\n", os.Getenv("OPENDEPLOY_E2E_SECRET"))
	printAssetMount("/tmp")
	checkIPv4Egress()
	printIssuedTLS()
	go serveIssuedTLS()
	go checkIssuedTLSClient()

	for count := 1; ; count++ {
		fmt.Printf("nixdockerbuild1 count=%d time=%s\n", count, time.Now().Format(time.RFC3339))
		time.Sleep(10 * time.Second)
	}
}

func checkIPv4Egress() {
	url := os.Getenv("OPENDEPLOY_E2E_IPV4_EGRESS_URL")
	if url == "" {
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		fmt.Printf("nixdockerbuild1 ipv4 egress error=%v\n", err)
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Printf("nixdockerbuild1 ipv4 egress error=%v\n", err)
		return
	}
	fmt.Printf("nixdockerbuild1 ipv4 egress observed source=%s status=%d\n", strings.TrimSpace(string(body)), response.StatusCode)
}

func issuedTLSDir() string {
	return os.Getenv("OPENDEPLOY_E2E_ISSUED_TLS_DIR")
}

func printIssuedTLS() {
	dir := issuedTLSDir()
	if dir == "" {
		return
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		fmt.Printf("nixdockerbuild1 issuedtls dir error=%v\n", err)
		return
	}
	files := map[string][]byte{"ca.crt": caPEM}
	fmt.Printf("nixdockerbuild1 issuedtls file ca.crt ok=true bytes=%d\n", len(caPEM))
	leafMissing := 0
	for _, name := range []string{"public.crt", "private.key"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			fmt.Printf("nixdockerbuild1 issuedtls file %s present=false\n", name)
			leafMissing++
			continue
		}
		if err != nil {
			fmt.Printf("nixdockerbuild1 issuedtls dir error=%v\n", err)
			return
		}
		files[name] = content
		fmt.Printf("nixdockerbuild1 issuedtls file %s ok=true bytes=%d\n", name, len(content))
	}
	if leafMissing == 2 {
		fmt.Printf("nixdockerbuild1 issuedtls mode=ca-only\n")
		return
	}
	if leafMissing != 0 {
		fmt.Printf("nixdockerbuild1 issuedtls error=partial leaf material\n")
		return
	}
	if _, err := tls.X509KeyPair(files["public.crt"], files["private.key"]); err != nil {
		fmt.Printf("nixdockerbuild1 issuedtls keypair error=%v\n", err)
		return
	}
	fmt.Printf("nixdockerbuild1 issuedtls keypair=ok\n")
	block, _ := pem.Decode(files["public.crt"])
	if block == nil {
		fmt.Printf("nixdockerbuild1 issuedtls cert error=not pem\n")
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		fmt.Printf("nixdockerbuild1 issuedtls cert error=%v\n", err)
		return
	}
	fmt.Printf("nixdockerbuild1 issuedtls subject=%s\n", cert.Subject.CommonName)
	for _, name := range cert.DNSNames {
		fmt.Printf("nixdockerbuild1 issuedtls san dns=%s\n", name)
	}
	fmt.Printf("nixdockerbuild1 issuedtls san ipcount=%d\n", len(cert.IPAddresses))
	fingerprint := sha256.Sum256(cert.Raw)
	fmt.Printf("nixdockerbuild1 issuedtls fingerprint=%s\n", hex.EncodeToString(fingerprint[:]))
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(files["ca.crt"]) {
		fmt.Printf("nixdockerbuild1 issuedtls ca error=not pem\n")
		return
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: cert.Subject.CommonName}); err != nil {
		fmt.Printf("nixdockerbuild1 issuedtls chain error=%v\n", err)
		return
	}
	fmt.Printf("nixdockerbuild1 issuedtls chain verified=true name=%s\n", cert.Subject.CommonName)
}

func serveIssuedTLS() {
	port := os.Getenv("OPENDEPLOY_E2E_ISSUED_TLS_SERVE_PORT")
	dir := issuedTLSDir()
	if port == "" || dir == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "issued-tls-server ok")
	})
	for {
		err := http.ListenAndServeTLS(":"+port, filepath.Join(dir, "public.crt"), filepath.Join(dir, "private.key"), mux)
		fmt.Printf("nixdockerbuild1 issuedtls serve error=%v\n", err)
		time.Sleep(5 * time.Second)
	}
}

func checkIssuedTLSClient() {
	host := os.Getenv("OPENDEPLOY_E2E_ISSUED_TLS_CONNECT_HOST")
	port := os.Getenv("OPENDEPLOY_E2E_ISSUED_TLS_CONNECT_PORT")
	serverName := os.Getenv("OPENDEPLOY_E2E_ISSUED_TLS_SERVER_NAME")
	dir := issuedTLSDir()
	if host == "" || port == "" || serverName == "" || dir == "" {
		return
	}
	for {
		if err := issuedTLSClientRequest(host, port, serverName, dir); err != nil {
			fmt.Printf("nixdockerbuild1 issuedtls client error=%v\n", err)
			time.Sleep(5 * time.Second)
			continue
		}
		return
	}
}

func issuedTLSClientRequest(host, port, serverName, dir string) error {
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("ca.crt is not pem")
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName}},
	}
	response, err := client.Get(fmt.Sprintf("https://%s/", net.JoinHostPort(host, port)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	fmt.Printf("nixdockerbuild1 issuedtls client verified=true status=%d body=%s\n", response.StatusCode, strings.TrimSpace(string(body)))
	return nil
}

func printAssetMount(root string) {
	info, err := os.Stat(root)
	if err != nil {
		fmt.Printf("nixdockerbuild1 asset root %s error=%v\n", root, err)
		return
	}
	if !info.IsDir() {
		fmt.Printf("nixdockerbuild1 asset root %s is not a directory\n", root)
		return
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("nixdockerbuild1 asset walk %s error=%v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fmt.Printf("nixdockerbuild1 asset file %s\n", rel)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			fmt.Printf("nixdockerbuild1 asset read %s error=%v\n", rel, readErr)
			return nil
		}
		fmt.Printf("nixdockerbuild1 asset content %s=%s\n", rel, strings.TrimSpace(string(content)))
		return nil
	})
	if err != nil {
		fmt.Printf("nixdockerbuild1 asset walk root error=%v\n", err)
	}
}
