package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"metabib/config"
)

func TestImportWorkerLimitAndCompletion(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{0, 1, 2, 20} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			core, logs := observer.New(zap.DebugLevel)
			var output bytes.Buffer
			importer := NewImporter(config.DatabaseConfig{ImportWorkers: workers}, "", zap.New(core), &output, true, true)
			dumps := []DumpFile{{Name: "first"}, {Name: "second"}, {Name: "third"}, {Name: "fourth"}}
			limit := min(max(workers, 1), len(dumps))
			started := make(chan struct{}, len(dumps))
			release := make(chan struct{})
			done := make(chan error, 1)
			var mu sync.Mutex
			var active, peak int
			var order []string
			go func() {
				done <- importer.importDumps(ctx, dumps, func(ctx context.Context, dump DumpFile) error {
					mu.Lock()
					active++
					peak = max(peak, active)
					order = append(order, dump.Name)
					mu.Unlock()
					defer func() { mu.Lock(); active--; mu.Unlock() }()
					started <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					for range 50 {
						if _, err := importer.logOut.Write([]byte(dump.Name + "\n")); err != nil {
							return err
						}
					}
					return nil
				})
			}()
			for range limit {
				select {
				case <-started:
				case <-ctx.Done():
					cancel()
					<-done
					t.Fatal("configured imports did not start concurrently")
				}
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if active != 0 || peak != limit {
				t.Fatalf("active=%d peak=%d, want 0 and %d", active, peak, limit)
			}
			if limit == 1 && !slices.Equal(order, []string{"first", "second", "third", "fourth"}) {
				t.Fatalf("sequential import order = %v", order)
			}
			for _, dump := range dumps {
				if count := bytes.Count(output.Bytes(), []byte(dump.Name+"\n")); count != 50 {
					t.Fatalf("client output for %s has %d lines, want 50", dump.Name, count)
				}
			}
			entries := logs.FilterMessage("SQL import completed").All()
			if len(entries) != 1 || entries[0].ContextMap()["workers"] != int64(limit) {
				t.Fatalf("completion log = %#v", entries)
			}
			if logs.FilterMessage("SQL dump import progress").Len() != len(dumps) {
				t.Fatal("missing per-file completion logs")
			}
		})
	}
}

func TestImportFailureCancelsActiveAndQueuedDumps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	core, logs := observer.New(zap.DebugLevel)
	importer := NewImporter(config.DatabaseConfig{ImportWorkers: 2}, "", zap.New(core), nil, false, true)
	want := errors.New("SQL import failed")
	secondStarted := make(chan struct{})
	var canceled atomic.Bool
	var calls atomic.Int64
	err := importer.importDumps(ctx, []DumpFile{{Name: "fail"}, {Name: "active"}, {Name: "queued"}},
		func(ctx context.Context, dump DumpFile) error {
			calls.Add(1)
			switch dump.Name {
			case "fail":
				select {
				case <-secondStarted:
					return want
				case <-ctx.Done():
					return ctx.Err()
				}
			case "active":
				close(secondStarted)
				<-ctx.Done()
				canceled.Store(true)
				return ctx.Err()
			default:
				return errors.New("queued import started after failure")
			}
		})
	if !errors.Is(err, want) || !canceled.Load() || calls.Load() != 2 {
		t.Fatalf("error=%v canceled=%v calls=%d", err, canceled.Load(), calls.Load())
	}
	if logs.FilterMessage("SQL import completed").Len() != 0 {
		t.Fatal("failed import logged successful completion")
	}
}

func TestImportContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	importer := NewImporter(config.DatabaseConfig{ImportWorkers: 2}, "", nil, nil, false, true)
	started := make(chan struct{}, 2)
	done := make(chan error, 1)
	go func() {
		done <- importer.importDumps(ctx, []DumpFile{{}, {}, {}}, func(ctx context.Context, _ DumpFile) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			<-done
			t.Fatal("imports did not start")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import error = %v", err)
	}
}
