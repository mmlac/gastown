package patrol

import (
	"context"
	"log/slog"
	"testing"
)

// testEnv returns an Env suitable for unit tests.
func testEnv(t *testing.T) Env {
	t.Helper()
	return Env{
		TownRoot: t.TempDir(),
		Logger:   slog.Default(),
	}
}

// testCtx returns a background context for tests.
func testCtx() context.Context {
	return context.Background()
}
