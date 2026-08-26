package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

const childStopTimeout = 20 * time.Second

type S3Config struct {
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
}

type replicatorProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	exitErr error

	mu         sync.Mutex
	status     childStatus
	statusSeen bool
}

func spawnReplicator(ctx context.Context, dbPath string, cfg S3Config) (*replicatorProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve own executable for backup replication process: %w", err)
	}
	cmd := exec.Command(exe, string(ainit.CommandLitestream))
	setParentDeathSignal(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start backup replication process: %w", err)
	}

	p := &replicatorProcess{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	readersDone := make(chan struct{}, 2)
	go func() {
		p.readStatus(stdout)
		readersDone <- struct{}{}
	}()
	go func() {
		relayChildLogs(ctx, stderr)
		readersDone <- struct{}{}
	}()
	go func() {
		<-readersDone
		<-readersDone
		p.exitErr = cmd.Wait()
		close(p.done)
	}()

	job := childJob{Mode: childModeReplicate, DBPath: dbPath, S3: cfg}
	if err := json.NewEncoder(stdin).Encode(job); err != nil {
		_ = cmd.Process.Kill()
		<-p.done
		return nil, fmt.Errorf("send job to backup replication process: %w", err)
	}
	return p, nil
}

func (p *replicatorProcess) readStatus(r io.Reader) {
	dec := json.NewDecoder(r)
	for {
		var status childStatus
		if err := dec.Decode(&status); err != nil {
			return
		}
		p.mu.Lock()
		p.status = status
		p.statusSeen = true
		p.mu.Unlock()
	}
}

func (p *replicatorProcess) lastStatus() (childStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.statusSeen
}

func (p *replicatorProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *replicatorProcess) stop() error {
	if p.exited() {
		return nil
	}
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return p.exitErr
	case <-time.After(childStopTimeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		return fmt.Errorf("backup replication process did not exit after stdin close; killed")
	}
}

func relayChildLogs(ctx context.Context, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			slog.InfoContext(ctx, string(line))
			continue
		}
		level := slog.LevelInfo
		if levelText, ok := entry["level"].(string); ok {
			_ = level.UnmarshalText([]byte(levelText))
		}
		msg, _ := entry["msg"].(string)
		attrs := make([]slog.Attr, 0, len(entry))
		for k, v := range entry {
			if k == "time" || k == "level" || k == "msg" {
				continue
			}
			attrs = append(attrs, slog.Any(k, v))
		}
		slog.LogAttrs(ctx, level, msg, attrs...)
	}
}

func Restore(ctx context.Context, cfg S3Config, dbPath, outputPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable for backup restore process: %w", err)
	}
	job, err := json.Marshal(childJob{Mode: childModeRestore, DBPath: dbPath, OutputPath: outputPath, S3: cfg})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, string(ainit.CommandLitestream))
	setParentDeathSignal(cmd)
	cmd.Stdin = bytes.NewReader(job)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := lastChildLogDetail(stderr.Bytes()); detail != "" {
			return fmt.Errorf("restore primary database: %s", detail)
		}
		return fmt.Errorf("restore primary database: %w", err)
	}
	return nil
}

func lastChildLogDetail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if detail, ok := strings.CutPrefix(line, "error: "); ok {
			return detail
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			if msg, ok := entry["msg"].(string); ok && msg != "" {
				if errAttr, ok := entry["err"].(string); ok && errAttr != "" {
					return msg + ": " + errAttr
				}
				return msg
			}
		}
		return line
	}
	return ""
}
