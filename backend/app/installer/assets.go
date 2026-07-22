package installer

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

// Assets are embedded so the installer is a single self-contained binary — no
// fetching unit files from raw.githubusercontent at install time (which the
// shell installer did, coupling every install to GitHub availability and to the
// release tag's deploy/ contents).

//go:embed assets/opendeploy.service
var unitOpenDeploy []byte

//go:embed assets/opendeploy-containerd.service
var unitContainerd []byte

//go:embed assets/env.template
var envTemplate []byte

//go:embed assets/sudoers
var sudoersTemplate []byte

// renderContainerdConfig builds containerd's config.toml. gid scopes the gRPC
// socket to the opendeploy group so the unprivileged daemon can dial it.
func renderContainerdConfig(gid int) string {
	return "version = 3\n" +
		"root = \"" + containerdRoot + "\"\n" +
		"state = \"" + containerdState + "\"\n\n" +
		"[grpc]\n" +
		"  address = \"" + containerdSocket + "\"\n" +
		"  gid = " + itoa(gid) + "\n"
}

func renderEnvTemplate(opts installOptions) []byte {
	return applyEnvOverrides(envTemplate, opts)
}

func renderOpenDeployUnit(opts installOptions) []byte {
	role := strings.TrimSpace(opts.role)
	if role == "" {
		role = "primary"
	}
	return []byte(strings.ReplaceAll(string(unitOpenDeploy), "ExecStart=/var/lib/opendeploy/bin/opendeploy primary", "ExecStart=/var/lib/opendeploy/bin/opendeploy "+role))
}

func updateEnvFile(opts installOptions, own owner) error {
	content := envTemplate
	if existing, err := os.ReadFile(envFile); err == nil {
		content = existing
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err := writeFile(envFile, applyEnvOverrides(stripInitialEnvValues(content), opts), 0o640, own, false)
	if err == nil {
		info("updated %s", envFile)
	}
	return err
}

func applyEnvOverrides(content []byte, opts installOptions) []byte {
	values := map[string]string{}
	if opts.passkeyExtraOrigins != nil {
		values["OPENDEPLOY_PASSKEY_EXTRA_ORIGINS"] = *opts.passkeyExtraOrigins
	}
	if opts.clusterAddr != nil {
		values["OPENDEPLOY_PRIMARY_CLUSTER_ADDR"] = *opts.clusterAddr
	}
	if opts.enrollmentAddr != nil {
		values["OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR"] = *opts.enrollmentAddr
	}
	if opts.enrollmentFingerprint != nil {
		values["OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT"] = *opts.enrollmentFingerprint
	}
	if opts.primaryName != nil {
		values["OPENDEPLOY_PRIMARY_NAME"] = *opts.primaryName
	}
	if opts.underlayAddress != nil {
		values["OPENDEPLOY_UNDERLAY_ADDRESS"] = *opts.underlayAddress
	}
	return applyEnvValues(content, values, []string{"OPENDEPLOY_PASSKEY_EXTRA_ORIGINS", "OPENDEPLOY_PRIMARY_CLUSTER_ADDR", "OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR", "OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT", "OPENDEPLOY_PRIMARY_NAME", "OPENDEPLOY_UNDERLAY_ADDRESS"})
}

func stripInitialEnvValues(content []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	out := lines[:0]
	for _, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && strings.HasPrefix(strings.TrimSpace(key), "OPENDEPLOY_INITIAL_") {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func applyEnvValues(content []byte, values map[string]string, appendOrder []string) []byte {
	if len(values) == 0 {
		return content
	}

	seen := map[string]bool{}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value, replace := values[key]
		if !replace {
			continue
		}
		lines[i] = formatEnvLine(key, value)
		seen[key] = true
	}
	for _, key := range appendOrder {
		if value, ok := values[key]; ok && !seen[key] {
			lines = append(lines, formatEnvLine(key, value))
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func formatEnvLine(key string, value string) string {
	if strings.ContainsAny(value, " \t#'") {
		return key + "='" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return key + "=" + value
}

func validateEnvValue(name, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must not contain newlines", name)
	}
	return nil
}
