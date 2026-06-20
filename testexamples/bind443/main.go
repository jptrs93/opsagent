package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const addr = "0.0.0.0:443"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.Printf("bind443 starting addr=%s uid=%d gid=%d", addr, os.Getuid(), os.Getgid())

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("bind443 listen failed addr=%s err=%v", addr, err)
		os.Exit(1)
	}
	defer listener.Close()

	log.Printf("bind443 listen successful addr=%s actual=%s", addr, listener.Addr())

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
