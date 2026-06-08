package installer

import _ "embed"

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
