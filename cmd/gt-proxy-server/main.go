// gt-proxy-server is the mTLS proxy server for sandboxed polecat execution.
// It runs on the host and allows containers to call gt/bd and access git repos
// via authenticated, authorized HTTP endpoints.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/steveyegge/gastown/internal/proxy"
)

func main() {
	var (
		listen      = flag.String("listen", "0.0.0.0:9876", "address to listen on")
		caDir       = flag.String("ca-dir", "", "directory for CA cert/key (default: ~/gt/.runtime/ca)")
		allowedCmds = flag.String("allowed-cmds", "gt,bd", "comma-separated list of allowed commands")
	)
	flag.Parse()

	if *caDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("cannot determine home dir: %v", err)
		}
		*caDir = filepath.Join(home, "gt", ".runtime", "ca")
	}

	ca, err := proxy.LoadOrGenerateCA(*caDir)
	if err != nil {
		log.Fatalf("CA setup failed: %v", err)
	}
	log.Printf("CA loaded from %s", *caDir)

	cmds := strings.Split(*allowedCmds, ",")
	for i := range cmds {
		cmds[i] = strings.TrimSpace(cmds[i])
	}

	cfg := proxy.Config{
		ListenAddr:      *listen,
		CAFile:          filepath.Join(*caDir, "ca.crt"),
		AllowedCommands: cmds,
	}

	srv := proxy.New(cfg, ca)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
