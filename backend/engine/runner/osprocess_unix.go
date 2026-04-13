package runner

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

func signalDaemonTerminate(pid int) error {
	return signalDaemon(pid, syscall.SIGTERM)
}

func signalDaemonKill(pid int) error {
	return signalDaemon(pid, syscall.SIGKILL)
}

func signalDaemon(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return err
		}
		if directErr := syscall.Kill(pid, sig); directErr != nil {
			return directErr
		}
	}
	return nil
}

func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

func processExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

// awaitProcessExit blocks until a child process exits and reaps it via Wait4.
// This must only be used for processes that opsagent spawned (i.e. direct
// children from ForkExec). For reattached processes, use poll-based monitoring.
func awaitProcessExit(pid int) {
	var ws syscall.WaitStatus
	for {
		_, err := syscall.Wait4(pid, &ws, 0, nil)
		if err == nil {
			return
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		// ECHILD or any other error: the process is no longer ours to wait on.
		return
	}
}

// spawnDaemon fork/execs the binary as a fully detached daemon (new session
// leader). Stdout and stderr are redirected to outputPath. If runAs is non-empty,
// the process runs as that OS user (requires opsagent to have CAP_SETUID or
// run as root). If runAs is empty, the process inherits opsagent's user.
func spawnDaemon(binPath, workDir, logPath, runAs string) (int, error) {
	slog.Info("spawnDaemon", "bin", binPath, "workDir", workDir, "logPath", logPath, "runAs", runAs)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("opening log file %q: %w", logPath, err)
	}
	defer logFile.Close()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	sysproc := &syscall.SysProcAttr{Setsid: true}
	env := scrubOpsagentEnv(os.Environ())
	if runAs != "" {
		u, err := user.Lookup(runAs)
		if err != nil {
			return 0, fmt.Errorf("looking up user %q: %w", runAs, err)
		}
		cred, err := uidGidCredential(u)
		if err != nil {
			return 0, fmt.Errorf("parsing credential for %q: %w", runAs, err)
		}
		sysproc.Credential = cred
		env = setEnv(env, "HOME", u.HomeDir)
		env = setEnv(env, "USER", u.Username)
	}

	pid, err := syscall.ForkExec(binPath, []string{binPath}, &syscall.ProcAttr{
		Dir: workDir,
		Env: env,
		Files: []uintptr{
			devNull.Fd(),
			logFile.Fd(),
			logFile.Fd(),
		},
		Sys: sysproc,
	})
	if err != nil {
		return 0, fmt.Errorf("fork/exec %q: %w", binPath, err)
	}
	return pid, nil
}

// uidGidCredential converts a looked-up user to a syscall.Credential.
func uidGidCredential(u *user.User) (*syscall.Credential, error) {
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parsing uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parsing gid %q: %w", u.Gid, err)
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

// setEnv replaces or appends a KEY=VALUE entry in an env slice.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// scrubOpsagentEnv removes OPSAGENT_* environment variables so we don't leak
// the master password hash, GitHub token, etc. into the deployed artifact.
func scrubOpsagentEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPSAGENT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
