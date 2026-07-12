package db

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"metabib/config"
)

func TestInstallDBArgs(t *testing.T) {
	t.Parallel()

	args := installDBArgs("/tmp/data")
	if !contains(args, "--datadir=/tmp/data") {
		t.Fatalf("installDBArgs() = %#v", args)
	}
	if runtime.GOOS == "windows" && contains(args, "--skip-test-db") {
		t.Fatalf("windows installDBArgs() contains unix option: %#v", args)
	}
	if runtime.GOOS != "windows" && !contains(args, "--skip-test-db") {
		t.Fatalf("unix installDBArgs() missing --skip-test-db: %#v", args)
	}
}

func TestBinaryHelpers(t *testing.T) {
	t.Parallel()

	names := binaryFileNames([]string{"mariadb"})
	if !contains(names, "mariadb") {
		t.Fatalf("binaryFileNames() = %#v", names)
	}
	if runtime.GOOS == "windows" && !contains(names, "mariadb.exe") {
		t.Fatalf("binaryFileNames() on windows = %#v", names)
	}
}

func TestFindAdminBinaryUsesPreparedPath(t *testing.T) {
	t.Parallel()

	rt := &Runtime{Admin: "/custom/mariadb-admin"}
	path, err := rt.findAdminBinary()
	if err != nil {
		t.Fatalf("findAdminBinary() error = %v", err)
	}
	if path != rt.Admin {
		t.Fatalf("findAdminBinary() = %q, want %q", path, rt.Admin)
	}
}

func TestWalkBinaryCandidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	bin := filepath.Join(root, "mariadb", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(bin, "mariadb")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	candidates := walkBinaryCandidates(root, []string{"mariadb"})
	if len(candidates) != 1 || candidates[0] != path {
		t.Fatalf("walkBinaryCandidates() = %#v", candidates)
	}
}

func TestPrepareManagedPathsUnixAndTCP(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	rt := &Runtime{Config: config.DatabaseConfig{
		Protocol: "unix",
		DataDir:  filepath.Join(base, "data"),
	}}
	if err := rt.prepareManagedPaths(); err != nil {
		t.Fatalf("prepareManagedPaths(unix) error = %v", err)
	}
	if rt.Config.Protocol != "unix" || rt.Config.Port != 0 || rt.Config.Socket == "" || rt.Config.Host != rt.Config.Socket {
		t.Fatalf("unix config = %#v", rt.Config)
	}
	if rt.Config.Socket != filepath.Join(base, "metabib.sock") ||
		rt.Config.PIDFile != filepath.Join(base, "metabib.pid") ||
		rt.Config.LogFile != filepath.Join(base, "metabib-mariadb.log") {
		t.Fatalf("unix support paths = socket %q pid %q log %q", rt.Config.Socket, rt.Config.PIDFile, rt.Config.LogFile)
	}

	rt = &Runtime{Config: config.DatabaseConfig{
		Protocol: "tcp",
		Host:     "127.0.0.1",
		Port:     3307,
		DataDir:  filepath.Join(base, "tcp-data"),
	}}
	if err := rt.prepareManagedPaths(); err != nil {
		t.Fatalf("prepareManagedPaths(tcp) error = %v", err)
	}
	if rt.Config.Protocol != "tcp" || rt.Config.Host != "127.0.0.1" || rt.Config.Port != 3307 || rt.Config.Socket != "" {
		t.Fatalf("tcp config = %#v", rt.Config)
	}
}

func TestPrepareManagedPathsTemporaryDerivesSupportPaths(t *testing.T) {
	t.Parallel()

	rt := &Runtime{Config: config.DatabaseConfig{
		Protocol:  "unix",
		Temporary: true,
		DataDir:   filepath.Join(t.TempDir(), "ignored"),
	}}
	t.Cleanup(func() {
		if rt.tmpDir != "" {
			if err := os.RemoveAll(rt.tmpDir); err != nil {
				t.Fatalf("remove temp dir: %v", err)
			}
		}
	})

	if err := rt.prepareManagedPaths(); err != nil {
		t.Fatalf("prepareManagedPaths() error = %v", err)
	}
	base := filepath.Join(rt.tmpDir, "data")
	if rt.Config.DataDir != base {
		t.Fatalf("DataDir = %q, want %q", rt.Config.DataDir, base)
	}
	if rt.Config.Socket != filepath.Join(rt.tmpDir, "metabib.sock") ||
		rt.Config.PIDFile != filepath.Join(rt.tmpDir, "metabib.pid") ||
		rt.Config.LogFile != filepath.Join(rt.tmpDir, "metabib-mariadb.log") {
		t.Fatalf("temporary support paths = socket %q pid %q log %q", rt.Config.Socket, rt.Config.PIDFile, rt.Config.LogFile)
	}
}

func TestPrepareManagedPathsTemporaryCreatesSupportDirs(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	rt := &Runtime{Config: config.DatabaseConfig{
		Protocol:  "unix",
		Temporary: true,
		Socket:    filepath.Join(base, "socket", "metabib.sock"),
		PIDFile:   filepath.Join(base, "pid", "metabib.pid"),
		LogFile:   filepath.Join(base, "log", "metabib-mariadb.log"),
	}}
	t.Cleanup(func() {
		if rt.tmpDir != "" {
			if err := os.RemoveAll(rt.tmpDir); err != nil {
				t.Fatalf("remove temp dir: %v", err)
			}
		}
	})

	if err := rt.prepareManagedPaths(); err != nil {
		t.Fatalf("prepareManagedPaths() error = %v", err)
	}
	for _, path := range []string{rt.Config.Socket, rt.Config.PIDFile, rt.Config.LogFile} {
		if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
			t.Fatalf("support directory for %q not ready: info=%v err=%v", path, info, err)
		}
	}
	if !strings.HasPrefix(rt.Config.DataDir, rt.tmpDir+string(os.PathSeparator)) {
		t.Fatalf("DataDir = %q, want under %q", rt.Config.DataDir, rt.tmpDir)
	}
}

func TestPrepareManagedPathsRejectsNonLoopbackTCP(t *testing.T) {
	t.Parallel()

	rt := &Runtime{Config: config.DatabaseConfig{
		Protocol: "tcp",
		Host:     "0.0.0.0",
		Port:     3307,
		DataDir:  filepath.Join(t.TempDir(), "data"),
	}}
	if err := rt.prepareManagedPaths(); err == nil {
		t.Fatal("prepareManagedPaths() error = nil, want non-loopback host rejection")
	}
}

func TestRemoveStaleSocket(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.sock")
	if err := removeStaleSocket(missing); err != nil {
		t.Fatalf("removeStaleSocket(missing) error = %v", err)
	}

	regular := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regular, []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := removeStaleSocket(regular); err == nil {
		t.Fatal("removeStaleSocket(regular) error = nil")
	}
	if _, err := os.Stat(regular); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	socket := filepath.Join(t.TempDir(), "stale.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen(unix) error = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close listener error = %v", err)
	}
	if err := removeStaleSocket(socket); err != nil {
		t.Fatalf("removeStaleSocket(socket) error = %v", err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket stat error = %v, want not exist", err)
	}
}

func TestStartManagedProcessRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd, _ := managedRuntimeHelperCommand(t, "graceful")
	rt := &Runtime{}
	if err := rt.startManagedProcess(ctx, cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("startManagedProcess() error = %v, want context.Canceled", err)
	}
	if cmd.Process != nil {
		t.Fatal("startManagedProcess() started process for canceled context")
	}
}

func TestManagedProcessSurvivesCancellationAndClosesGracefully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not supported for child processes on Windows")
	}
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	rt, ready := managedTestRuntime(t, "graceful", 2*time.Second)
	if err := rt.startManagedProcess(ctx, rt.cmd); err != nil {
		t.Fatalf("startManagedProcess() error = %v", err)
	}
	waitManagedRuntimeHelper(t, ready)
	cancel()
	select {
	case err := <-rt.waitCh:
		t.Fatalf("managed process exited on context cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if rt.cmd.ProcessState == nil || !rt.cmd.ProcessState.Exited() {
		t.Fatal("managed process was not reaped")
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestManagedProcessTimeoutKillsAndReaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal behavior differs on Windows")
	}
	t.Parallel()

	rt, ready := managedTestRuntime(t, "ignore", 100*time.Millisecond)
	if err := rt.startManagedProcess(context.Background(), rt.cmd); err != nil {
		t.Fatalf("startManagedProcess() error = %v", err)
	}
	waitManagedRuntimeHelper(t, ready)
	err := rt.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want context.DeadlineExceeded", err)
	}
	if rt.cmd.ProcessState == nil {
		t.Fatal("timed-out managed process was not reaped")
	}
}

func TestUnmanagedRuntimeDoesNotStopProcess(t *testing.T) {
	t.Parallel()

	cmd, ready := managedRuntimeHelperCommand(t, "ignore")
	rt := &Runtime{cmd: cmd, LogOut: io.Discard}
	if err := rt.startManagedProcess(context.Background(), cmd); err != nil {
		t.Fatalf("startManagedProcess() error = %v", err)
	}
	waitManagedRuntimeHelper(t, ready)
	if err := rt.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-rt.waitCh:
		t.Fatalf("unmanaged process exited during Close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill unmanaged test process: %v", err)
	}
	<-rt.waitCh
	if cmd.ProcessState == nil {
		t.Fatal("unmanaged test process was not reaped")
	}
}

func TestManagedRuntimeHelperProcess(t *testing.T) {
	mode := os.Getenv("METABIB_MANAGED_RUNTIME_HELPER")
	if mode == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("METABIB_MANAGED_RUNTIME_READY"), nil, 0o644); err != nil {
		t.Fatalf("write helper readiness marker: %v", err)
	}
	if mode == "ignore" {
		signal.Ignore(os.Interrupt)
		for {
			time.Sleep(time.Hour)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	<-signals
}

func managedTestRuntime(t *testing.T, mode string, timeout time.Duration) (*Runtime, string) {
	t.Helper()
	cmd, ready := managedRuntimeHelperCommand(t, mode)
	return &Runtime{
		Config:          config.DatabaseConfig{Protocol: "unix", User: "root"},
		LogOut:          io.Discard,
		managed:         true,
		cmd:             cmd,
		findAdmin:       func() (string, error) { return "", errors.New("admin unavailable") },
		shutdownTimeout: timeout,
	}, ready
}

func managedRuntimeHelperCommand(t *testing.T, mode string) (*exec.Cmd, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedRuntimeHelperProcess$")
	cmd.Env = append(
		os.Environ(),
		"METABIB_MANAGED_RUNTIME_HELPER="+mode,
		"METABIB_MANAGED_RUNTIME_READY="+ready,
	)
	return cmd, ready
}

func waitManagedRuntimeHelper(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("managed runtime helper did not become ready")
}
