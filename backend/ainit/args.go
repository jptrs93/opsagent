package ainit

import (
	"fmt"
	"io"
	"os"

	"github.com/jptrs93/goutil/envu"
)

type Command string

const (
	CommandPrimary        Command = "primary"
	CommandSecondary      Command = "secondary"
	CommandInstall        Command = "install"
	CommandUninstall      Command = "uninstall"
	CommandRawLogConsumer Command = "raw-binary-log-consumer"
	CommandNetproxy       Command = "dataplane"
	CommandLitestream     Command = "litestream"
	commandTest           Command = "test"
)

type Arguments struct {
	Program   string
	Command   Command
	Installer bool
}

var Args Arguments

func initArgs() {
	Args = Arguments{Program: programName()}
	if envu.IsTestBasedOnArgs() {
		Args.Command = commandTest
		return
	}
	if len(os.Args) < 2 {
		usage(os.Stderr, Args.Program)
		os.Exit(2)
	}

	switch Command(os.Args[1]) {
	case CommandPrimary:
		Args.Command = CommandPrimary
	case CommandSecondary:
		Args.Command = CommandSecondary
	case CommandInstall:
		Args.Command = CommandInstall
		Args.Installer = true
	case CommandUninstall:
		Args.Command = CommandUninstall
		Args.Installer = true
	case CommandRawLogConsumer:
		Args.Command = CommandRawLogConsumer
	case CommandNetproxy:
		Args.Command = CommandNetproxy
	case CommandLitestream:
		Args.Command = CommandLitestream
	default:
		usage(os.Stderr, Args.Program)
		fmt.Fprintf(os.Stderr, "\nunknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func programName() string {
	if len(os.Args) == 0 || os.Args[0] == "" {
		return "opendeploy"
	}
	return os.Args[0]
}

func usage(w io.Writer, prog string) {
	fmt.Fprintf(w, `%[1]s - deployment management server
Usage:
  %[1]s primary
  %[1]s secondary
  %[1]s install primary [--version vX.Y.Z|latest] [--http-only true] [--password-login true] [--web-listen :8080] [--web-tls-self-managed true] [--web-tls-cert-pem-file cert.pem] [--web-hosts host1,host2] [--acme-hosts host1,host2] [--primary-name primary] [--dry-run]
  %[1]s install secondary --cluster-addr host:9443 --enrollment-addr host:9444 --enrollment-fingerprint sha256:<hex> [--version vX.Y.Z|latest] [--primary-name primary] [--dry-run]
  %[1]s uninstall [--purge] [--yes] [--dry-run]
  %[1]s dataplane

Commands:
  primary     Run the primary HTTP server and cluster listeners.
  secondary   Run a secondary that enrolls with and connects to the primary.
  install     Fresh install or in-place upgrade.
  uninstall   Stop services and containers, remove network state, units, and binary; --purge also wipes all data.
  dataplane    Internal netproxy process.
`, prog)
}
