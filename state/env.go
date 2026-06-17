package state

import (
	"context"
	"io"
	"time"

	"go.uber.org/zap"

	"metabib/config"
)

type envKey struct{}

type LocalEnv struct {
	Cfg   *config.Config
	Log   *zap.Logger
	LogIO io.WriteCloser
	start time.Time
}

func EnvFromContext(ctx context.Context) *LocalEnv {
	if env, ok := ctx.Value(envKey{}).(*LocalEnv); ok {
		return env
	}
	panic("local env not found in context")
}

func ContextWithEnv(ctx context.Context) context.Context {
	return context.WithValue(ctx, envKey{}, &LocalEnv{start: time.Now()})
}

func (e *LocalEnv) Uptime() time.Duration {
	return time.Since(e.start)
}

func (e *LocalEnv) Close() error {
	if e.Log != nil {
		_ = e.Log.Sync()
	}
	if e.LogIO != nil {
		return e.LogIO.Close()
	}
	return nil
}
