package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const addr = "0.0.0.0:443"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.Printf("bind443 starting addr=%s uid=%d gid=%d", addr, os.Getuid(), os.Getgid())
	logPort443Listeners("before-listen")

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("bind443 listen failed addr=%s err=%v", addr, err)
		logPort443Listeners("listen-failed")
		os.Exit(1)
	}
	defer listener.Close()

	log.Printf("bind443 listen successful addr=%s actual=%s", addr, listener.Addr())
	logPort443Listeners("listen-success")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go acceptLoop(listener)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("bind443 stopping")
			return
		case now := <-ticker.C:
			log.Printf("bind443 still listening addr=%s time=%s", listener.Addr(), now.UTC().Format(time.RFC3339))
		}
	}
}

type tcpListener struct {
	proto string
	local string
	uid   string
	inode string
}

type socketProcess struct {
	pid     string
	comm    string
	cmdline string
}

func logPort443Listeners(phase string) {
	listeners := append(readTCPListeners("/proc/net/tcp", "tcp4"), readTCPListeners("/proc/net/tcp6", "tcp6")...)
	if len(listeners) == 0 {
		log.Printf("bind443 port scan phase=%s result=no-listeners", phase)
		return
	}

	processes := socketProcesses()
	for _, listener := range listeners {
		owners := processes[listener.inode]
		if len(owners) == 0 {
			log.Printf("bind443 port scan phase=%s proto=%s local=%s uid=%s inode=%s visible_process=none note=socket-owner-not-visible-in-this-pid-namespace",
				phase, listener.proto, listener.local, listener.uid, listener.inode)
			continue
		}
		for _, owner := range owners {
			log.Printf("bind443 port scan phase=%s proto=%s local=%s uid=%s inode=%s pid=%s comm=%q cmdline=%q",
				phase, listener.proto, listener.local, listener.uid, listener.inode, owner.pid, owner.comm, owner.cmdline)
		}
	}
}

func readTCPListeners(path, proto string) []tcpListener {
	file, err := os.Open(path)
	if err != nil {
		log.Printf("bind443 port scan read failed path=%s err=%v", path, err)
		return nil
	}
	defer file.Close()

	var listeners []tcpListener
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[0] == "sl" {
			continue
		}
		local := fields[1]
		state := fields[3]
		uid := fields[7]
		inode := fields[9]
		if state != "0A" || !strings.HasSuffix(local, ":01BB") || inode == "0" {
			continue
		}
		listeners = append(listeners, tcpListener{proto: proto, local: local, uid: uid, inode: inode})
	}
	if err := scanner.Err(); err != nil {
		log.Printf("bind443 port scan scanner failed path=%s err=%v", path, err)
	}
	return listeners
}

func socketProcesses() map[string][]socketProcess {
	result := map[string][]socketProcess{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		log.Printf("bind443 process scan failed path=/proc err=%v", err)
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid := entry.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", pid, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		proc := socketProcess{pid: pid, comm: readTrimmed(filepath.Join("/proc", pid, "comm")), cmdline: readCmdline(filepath.Join("/proc", pid, "cmdline"))}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			result[inode] = append(result[inode], proc)
		}
	}
	return result
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readCmdline(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	cmdline := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.TrimSpace(cmdline)
}

func acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("bind443 accept failed err=%v", err)
			continue
		}
		log.Printf("bind443 accepted connection remote=%s local=%s", conn.RemoteAddr(), conn.LocalAddr())
		_ = conn.Close()
	}
}
