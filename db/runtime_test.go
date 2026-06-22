package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
