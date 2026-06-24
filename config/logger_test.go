package config

import (
	"errors"
	"io"
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

func TestLoggingPrepareProcessOutputUsesConsole(t *testing.T) {
	data := captureProcessConsoleOutput(t, "debug")
	if !strings.Contains(data, "mariadb console line") {
		t.Fatalf("console log did not contain process output: %s", data)
	}
}

func TestLoggingPrepareProcessOutputHiddenFromNormalConsole(t *testing.T) {
	data := captureProcessConsoleOutput(t, "normal")
	if strings.Contains(data, "mariadb console line") {
		t.Fatalf("normal console log contained debug process output: %s", data)
	}
}

func captureProcessConsoleOutput(t *testing.T, consoleLevel string) string {
	t.Helper()

	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = writePipe
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = readPipe.Close()
	})

	logger, processLog, err := (&LoggingConfig{
		ConsoleLogger: LoggerConfig{Level: consoleLevel},
		FileLogger:    LoggerConfig{Level: "none"},
	}).Prepare("metabib-test")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := processLog.Write([]byte("mariadb console line\n")); err != nil {
		t.Fatalf("processLog.Write() error = %v", err)
	}
	_ = logger.Sync()
	if err := processLog.Close(); err != nil {
		t.Fatalf("processLog.Close() error = %v", err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatalf("writePipe.Close() error = %v", err)
	}

	data, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
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
