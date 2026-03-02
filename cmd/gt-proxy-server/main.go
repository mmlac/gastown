// gt-proxy-server is the mTLS proxy server for sandboxed polecat execution.
// It runs on the host and allows containers to call gt/bd and access git repos
// via authenticated, authorized HTTP endpoints.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/steveyegge/gastown/internal/proxy"
)

// defaultAllowedSubcmds lists the safe subcommands for gt and bd.
// Dangerous subcommands (e.g. gt polecat, gt rig, gt admin, gt nuke) are excluded.
const defaultAllowedSubcmds = "" +
	"gt:prime,hook,done,mail,nudge,mol,status,handoff,version,convoy,sling;" +
	"bd:create,update,close,show,list,ready,dep,export,prime,stats,blocked,doctor"

func main() {
	var (
		listen         = flag.String("listen", "0.0.0.0:9876", "address to listen on")
		caDir          = flag.String("ca-dir", "", "directory for CA cert/key (default: ~/gt/.runtime/ca)")
		allowedCmds    = flag.String("allowed-cmds", "gt,bd", "comma-separated list of allowed commands")
		allowedSubcmds = flag.String("allowed-subcmds", defaultAllowedSubcmds,
			`semicolon-separated list of "cmd:sub1,sub2,..." subcommand allowlists`)
		townRoot = flag.String("town-root", "", "Gas Town root directory (default: $GT_TOWN or ~/gt)")
	)
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot determine home dir", "err", err)
		os.Exit(1)
	}

	if *caDir == "" {
		*caDir = filepath.Join(home, "gt", ".runtime", "ca")
	}

	if *townRoot == "" {
		if v := os.Getenv("GT_TOWN"); v != "" {
			*townRoot = v
		} else {
			*townRoot = filepath.Join(home, "gt")
		}
	}

	ca, err := proxy.LoadOrGenerateCA(*caDir)
	if err != nil {
		slog.Error("CA setup failed", "err", err)
		os.Exit(1)
	}
	slog.Info("CA loaded", "dir", *caDir)

	cmds := strings.Split(*allowedCmds, ",")
	for i := range cmds {
		cmds[i] = strings.TrimSpace(cmds[i])
	}

	cfg := proxy.Config{
		ListenAddr:         *listen,
		AllowedCommands:    cmds,
		AllowedSubcommands: parseAllowedSubcmds(*allowedSubcmds),
		TownRoot:           *townRoot,
	}

	srv := proxy.New(cfg, ca)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// parseAllowedSubcmds parses a string of the form
// "gt:prime,hook,done;bd:create,update,close" into a map of command → subcommand set.
func parseAllowedSubcmds(s string) map[string][]string {
	if s == "" {
		return nil
	}
	result := make(map[string][]string)
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx < 0 {
			continue
		}
		cmd := strings.TrimSpace(part[:idx])
		subsStr := strings.TrimSpace(part[idx+1:])
		var subs []string
		for _, sub := range strings.Split(subsStr, ",") {
			sub = strings.TrimSpace(sub)
			if sub != "" {
				subs = append(subs, sub)
			}
		}
		if cmd != "" && len(subs) > 0 {
			result[cmd] = subs
		}
	}
	return result
}
