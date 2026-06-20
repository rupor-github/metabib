package state

import (
	"context"
	"testing"
)

func TestContextWithEnv(t *testing.T) {
	t.Parallel()

	ctx := ContextWithEnv(context.Background())
	env := EnvFromContext(ctx)
	if env == nil {
		t.Fatal("EnvFromContext() returned nil")
	}
	if env.Uptime() < 0 {
		t.Fatal("Uptime() is negative")
	}
	if err := env.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
