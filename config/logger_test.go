package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLoggingPrepareFile(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "metabib.log")
	logger, processLog, err := (&LoggingConfig{
		ConsoleLogger: LoggerConfig{Level: "none"},
		FileLogger:    LoggerConfig{Level: "debug", Destination: logPath, Mode: "overwrite"},
	}).Prepare("metabib-test")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	logger.Info("hello")
	if _, err := processLog.Write([]byte("mariadb line\n")); err != nil {
		t.Fatalf("processLog.Write() error = %v", err)
	}
	_ = logger.Sync()
	if err := processLog.Close(); err != nil {
		t.Fatalf("processLog.Close() error = %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "mariadb line") {
		t.Fatalf("log file did not contain expected data: %s", data)
	}
}

func TestConsoleEncoderErrorField(t *testing.T) {
	t.Parallel()

	enc := newConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	buf, err := enc.EncodeEntry(zapcore.Entry{Message: "msg"}, []zapcore.Field{zap.Error(errors.New("boom"))})
	if err != nil {
		t.Fatalf("EncodeEntry() error = %v", err)
	}
	defer buf.Free()
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("encoded entry = %q", buf.String())
	}
}
