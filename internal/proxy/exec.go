package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// execRequest is the body for POST /v1/exec.
type execRequest struct {
	Argv []string `json:"argv"`
}

// execResponse is the response for POST /v1/exec.
type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit request body to prevent a misbehaving client from exhausting memory.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB

	// Extract identity from client cert CN (format: gt-<rig>-<name>).
	identity := extractIdentity(r)

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Argv) == 0 {
		http.Error(w, "argv is empty", http.StatusBadRequest)
		return
	}

	// Validate argv[0] is in the allowlist.
	cmd0 := req.Argv[0]
	if !s.isAllowed(cmd0) {
		http.Error(w, fmt.Sprintf("command not allowed: %q", cmd0), http.StatusForbidden)
		return
	}

	argv := req.Argv
	// Inject --identity <rig>/<name> immediately after argv[0] so that
	// "gt mail inbox --json --identity foo/bar" becomes well-formed.
	if identity != "" {
		argv = append([]string{argv[0], "--identity", identity}, argv[1:]...)
	}

	out, errOut, exitCode := runCommand(r.Context(), argv)

	// Issue 15: The handler always returns HTTP 200 even when the subprocess exits
	// non-zero. This is intentional: the RPC call itself succeeded (the request was
	// well-formed, the command was allowed, and the subprocess ran). The subprocess's
	// outcome is reported in the JSON body via exitCode. Callers must inspect exitCode
	// rather than the HTTP status to determine whether the command succeeded.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(execResponse{
		Stdout:   out,
		Stderr:   errOut,
		ExitCode: exitCode,
	})
}

// extractIdentity parses the client cert CN "gt-<rig>-<name>" into "<rig>/<name>".
// Uses LastIndex to correctly handle hyphenated rig names (e.g. "gas-town").
func extractIdentity(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	return cnToIdentity(cn)
}

// polecatName extracts the polecat name from a CN of the form "gt-<rig>-<name>".
// The last "-" is the rig/name separator, so hyphenated rig names are handled correctly.
// Returns "" if the CN does not match the expected format or the name is empty.
func polecatName(cn string) string {
	if !strings.HasPrefix(cn, "gt-") {
		return ""
	}
	rest := cn[3:] // strip "gt-"
	idx := strings.LastIndex(rest, "-")
	if idx < 0 {
		return ""
	}
	return rest[idx+1:]
}

// cnToIdentity converts a CN of the form "gt-<rig>-<name>" to "<rig>/<name>".
// The last "-" is treated as the rig/name separator, so hyphenated rig names
// (e.g. "gas-town") are handled correctly.
func cnToIdentity(cn string) string {
	name := polecatName(cn)
	if name == "" {
		return ""
	}
	// rig is everything between "gt-" and "-<name>".
	rest := cn[3:] // strip "gt-"
	rig := rest[:len(rest)-len(name)-1]
	if rig == "" {
		return ""
	}
	return rig + "/" + name
}

// isAllowed reports whether cmd is in the allowlist.
func (s *Server) isAllowed(cmd string) bool {
	return s.allowed[cmd]
}

func runCommand(ctx context.Context, argv []string) (stdout, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	// Restrict the subprocess environment to prevent server credentials from
	// leaking into gt/bd calls.
	cmd.Env = minimalEnv()
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}
