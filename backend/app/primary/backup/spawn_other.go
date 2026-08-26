//go:build !linux

package backup

import "os/exec"

func setParentDeathSignal(cmd *exec.Cmd) {}
