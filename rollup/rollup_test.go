package rollup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestNameToID(t *testing.T) {
	t.Parallel()

	if got := nameToID("123.fb2"); got != 123 {
		t.Fatalf("nameToID() = %d, want 123", got)
	}
	if got := nameToID("bad.fb2"); got != -1 {
		t.Fatalf("nameToID(bad) = %d, want -1", got)
	}
}

func TestCalendarBucketRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		now       time.Time
		bucket    CalendarBucket
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "iso week",
			now:       time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
			bucket:    CalendarBucketISOWeek,
			wantStart: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "iso biweek odd",
			now:       time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
			bucket:    CalendarBucketISOBiweek,
			wantStart: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "iso biweek week 53",
			now:       time.Date(2021, 1, 1, 12, 0, 0, 0, time.UTC),
			bucket:    CalendarBucketISOBiweek,
			wantStart: time.Date(2020, 12, 28, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "month",
			now:       time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
			bucket:    CalendarBucketMonth,
			wantStart: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStart, gotEnd := calendarBucketRange(tt.now, tt.bucket)
			if !gotStart.Equal(tt.wantStart) || !gotEnd.Equal(tt.wantEnd) {
				t.Fatalf("calendarBucketRange() = %v:%v, want %v:%v", gotStart, gotEnd, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestGetUpdates(t *testing.T) {
	t.Parallel()

	files := []archive{
		{info: fakeInfo{name: "f.fb2.000101-000150.zip"}},
		{info: fakeInfo{name: "f.fb2.000140-000160.zip"}},
		{info: fakeInfo{name: "f.fb2.000151-000200.zip"}},
		{info: fakeInfo{name: "f.pdf.000151-000200.zip"}},
		{info: fakeInfo{name: "2026-07-12.000201-000250.503.fb2.zip"}},
		{info: fakeInfo{name: "2026-07-12.000251-000300.503.pdf.zip"}},
		{info: fakeInfo{name: "f.fb2.000201-000250.zip.tmp"}},
		{info: fakeInfo{name: "backup-f.fb2.000251-000300.zip"}},
		{info: fakeInfo{name: "fb2-000001-000100.zip"}},
	}
	updates, err := getUpdates(files, 150, archiveFamily{Prefix: "fb2", Name: "fb2"}, mustCompileDefaultUpdatePatterns(t))
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 3 || updates[0].begin != 140 || updates[0].end != 160 || updates[1].begin != 151 || updates[1].end != 200 || updates[2].begin != 201 || updates[2].end != 250 {
		t.Fatalf("updates = %#v, want 140-160, 151-200, and 201-250", updates)
	}
}

func TestGetUSRUpdates(t *testing.T) {
	t.Parallel()

	files := []archive{
		{info: fakeInfo{name: "f.fb2.000101-000150.zip"}},
		{info: fakeInfo{name: "f.n.000101-000150.zip"}},
		{info: fakeInfo{name: "f.pdf.000151-000200.zip"}},
		{info: fakeInfo{name: "2026-07-12.000201-000250.503.fb2.zip"}},
		{info: fakeInfo{name: "2026-07-12.000251-000300.503.pdf.zip"}},
	}
	updates, err := getUpdates(files, 150, archiveFamily{Prefix: "usr", Name: "usr"}, mustCompileDefaultUpdatePatterns(t))
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 2 || updates[0].begin != 151 || updates[0].end != 200 || updates[1].begin != 251 || updates[1].end != 300 {
		t.Fatalf("updates = %#v, want 151-200 and 251-300", updates)
	}
}

func TestGetUpdatesUsesConfiguredPatterns(t *testing.T) {
	t.Parallel()

	patterns, err := compileUpdatePatterns([]UpdatePattern{{Name: "custom-usr", Family: "usr", Pattern: `(?i)^custom\.([0-9]+)-([0-9]+)\.zip$`}})
	if err != nil {
		t.Fatalf("compileUpdatePatterns() error = %v", err)
	}
	updates, err := getUpdates([]archive{{info: fakeInfo{name: "custom.000010-000020.zip"}}}, 0, archiveFamily{Prefix: "usr", Name: "usr"}, patterns)
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 1 || updates[0].begin != 10 || updates[0].end != 20 {
		t.Fatalf("updates = %#v, want custom 10-20", updates)
	}
}

func TestGetUpdatesRejectsAmbiguousPatterns(t *testing.T) {
	t.Parallel()

	patterns, err := compileUpdatePatterns([]UpdatePattern{
		{Name: "first", Family: "fb2", Pattern: `(?i)^f\.fb2\.([0-9]+)-([0-9]+)\.zip$`},
		{Name: "second", Family: "fb2", Pattern: `(?i)^f\.[^.]+\.([0-9]+)-([0-9]+)\.zip$`},
	})
	if err != nil {
		t.Fatalf("compileUpdatePatterns() error = %v", err)
	}
	_, err = getUpdates([]archive{{info: fakeInfo{name: "f.fb2.000010-000020.zip"}}}, 0, archiveFamily{Prefix: "fb2", Name: "fb2"}, patterns)
	if err == nil || !strings.Contains(err.Error(), "matches multiple rollup patterns") {
		t.Fatalf("getUpdates() error = %v, want ambiguity error", err)
	}
}

func TestLocalArchiveNamesRequireExactMatch(t *testing.T) {
	t.Parallel()

	files := []archive{
		{info: fakeInfo{name: "fb2-000001-000100.zip.tmp"}},
		{info: fakeInfo{name: "backup-fb2-000001-000200.zip"}},
		{info: fakeInfo{name: "fb2-000001-000300.merging.tmp"}},
		{info: fakeInfo{name: "fb2-000001-000400.zip"}},
	}
	last, err := getLastArchive(files, archiveFamily{Prefix: "fb2", Name: "fb2"})
	if err != nil {
		t.Fatalf("getLastArchive() error = %v", err)
	}
	if last.end != 400 {
		t.Fatalf("last.end = %d, want 400", last.end)
	}
	merge, err := getMergeArchive(files, archiveFamily{Prefix: "fb2", Name: "fb2"})
	if err != nil {
		t.Fatalf("getMergeArchive() error = %v", err)
	}
	if merge.info != nil {
		t.Fatalf("merge = %#v, want none", merge)
	}
}

func TestArchiveNameWidth(t *testing.T) {
	t.Parallel()

	fb2Family := archiveFamily{Prefix: "fb2", Name: "fb2"}
	if got := archiveNameWidth(archive{}, archive{}, fb2Family); got != 10 {
		t.Fatalf("archiveNameWidth(empty) = %d, want 10", got)
	}
	last := archive{info: fakeInfo{name: "fb2-000001-000100.zip"}}
	if got := archiveNameWidth(last, archive{}, fb2Family); got != 6 {
		t.Fatalf("archiveNameWidth(last) = %d, want 6", got)
	}
	merge := archive{info: fakeInfo{name: "fb2-0000000101-0000000200.merging"}}
	if got := archiveNameWidth(last, merge, fb2Family); got != 10 {
		t.Fatalf("archiveNameWidth(merge) = %d, want 10", got)
	}
}

func TestRunCreatesMergeArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 0 {
		t.Fatalf("Finalized = %d, want 0", res.Finalized)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	entries, err := countZipEntries(res.ActiveMerge)
	if err != nil {
		t.Fatalf("countZipEntries() error = %v", err)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2", entries)
	}
}

func TestRunCreatesFB2AndUSRMergeArchives(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})
	writeZip(t, filepath.Join(updates, "f.n.000001-000002.zip"), map[string]string{
		"1.pdf": "one",
		"2.pdf": "two",
	})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(res.ActiveMerges) != 2 {
		t.Fatalf("ActiveMerges = %#v, want two", res.ActiveMerges)
	}
	for _, want := range []string{"fb2-0000000001-0000000002.merging", "usr-0000000001-0000000002.merging"} {
		if _, err := os.Stat(filepath.Join(archives, want)); err != nil {
			t.Fatalf("stat %s: %v", want, err)
		}
	}
}

func TestRunKeepsUSRFormatsForSameBookID(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.pdf.000001-000001.zip"), map[string]string{
		"1.pdf":  "pdf",
		"1.djvu": "djvu",
	})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "usr-0000000001-0000000001.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	counts := countZipEntryNames(t, res.ActiveMerge)
	if counts["1.pdf"] != 1 || counts["1.djvu"] != 1 || len(counts) != 2 {
		t.Fatalf("entry counts = %#v, want pdf and djvu", counts)
	}
}

func TestRunUsesFamilyTargetSizes(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000001.zip"), map[string]string{"1.fb2": "one"})
	writeZip(t, filepath.Join(updates, "f.pdf.000001-000001.zip"), map[string]string{"1.pdf": "one"})

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: map[string]int64{"fb2": 1, "usr": 1_000_000},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized == 0 || len(res.FinalizedArchives) == 0 || filepath.Base(res.FinalizedArchives[0]) != "fb2-0000000001-0000000001.zip" {
		t.Fatalf("FinalizedArchives = %#v, want fb2 finalized", res.FinalizedArchives)
	}
	if len(res.ActiveMerges) != 1 || filepath.Base(res.ActiveMerges[0]) != "usr-0000000001-0000000001.merging" {
		t.Fatalf("ActiveMerges = %#v, want usr active merge", res.ActiveMerges)
	}
}

func TestRunFinalizesArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})

	res, err := Run(context.Background(), testOptions(archives, updates, 1))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized == 0 {
		t.Fatal("Finalized = 0, want at least one finalized archive")
	}
	if filepath.Base(res.FinalizedArchives[0]) != "fb2-0000000001-0000000001.zip" {
		t.Fatalf("first finalized archive = %q", res.FinalizedArchives[0])
	}
}

func TestRunRollingCreatesMergeStateAndIgnoresSize(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	startedAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000002.zip"), map[string]string{
		"1.fb2": "one",
		"2.fb2": "two",
	})

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1),
		Finalization: FinalizationOptions{
			Policy:          FinalizationPolicyRolling,
			RollingDuration: 14 * 24 * time.Hour,
			RollingText:     "14d",
		},
		Now: func() time.Time { return startedAt },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 0 {
		t.Fatalf("Finalized = %d, want 0 because period policy ignores size", res.Finalized)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	state, err := loadRollupState(filepath.Join(archives, stateFileName))
	if err != nil {
		t.Fatalf("loadRollupState() error = %v", err)
	}
	lineage, ok := state.Lineages["fb2"]
	if !ok {
		t.Fatalf("state lineages = %#v, want fb2", state.Lineages)
	}
	if lineage.Policy != FinalizationPolicyRolling || lineage.Rolling != "14d" {
		t.Fatalf("lineage = %#v, want rolling 14d", lineage)
	}
	if lineage.StartedAt == nil || !lineage.StartedAt.Equal(startedAt) {
		t.Fatalf("StartedAt = %v, want %v", lineage.StartedAt, startedAt)
	}
	deadline := startedAt.Add(14 * 24 * time.Hour)
	if lineage.Deadline == nil || !lineage.Deadline.Equal(deadline) {
		t.Fatalf("Deadline = %v, want %v", lineage.Deadline, deadline)
	}
}

func TestRunPeriodFailsWhenMergeStateMissing(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZip(t, filepath.Join(archives, "fb2-0000000001-0000000001.merging"), map[string]string{"1.fb2": "one"})

	_, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testRollingFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
	})
	if err == nil || !strings.Contains(err.Error(), "state has no lineage data") {
		t.Fatalf("Run() error = %v, want missing lineage state error", err)
	}
}

func TestRunRollingFinalizesExpiredMergeWithoutUpdates(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	deadline := startedAt.Add(14 * 24 * time.Hour)
	mergeName := "fb2-0000000001-0000000001.merging"
	writeZip(t, filepath.Join(archives, mergeName), map[string]string{"1.fb2": "one"})
	state := &rollupState{Version: 1, Lineages: map[string]rollupLineageState{
		"fb2": {
			ActiveMerge: mergeName,
			FirstBook:   1,
			LastBook:    1,
			Policy:      FinalizationPolicyRolling,
			Rolling:     "14d",
			StartedAt:   timePtr(startedAt),
			Deadline:    timePtr(deadline),
		},
	}}
	if err := saveRollupState(filepath.Join(archives, stateFileName), state); err != nil {
		t.Fatalf("saveRollupState() error = %v", err)
	}
	core, logs := observer.New(zap.InfoLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testRollingFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
		Now:             func() time.Time { return deadline },
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 1 || len(res.FinalizedArchives) != 1 || filepath.Base(res.FinalizedArchives[0]) != "fb2-0000000001-0000000001.zip" {
		t.Fatalf("FinalizedArchives = %#v finalized=%d, want expired merge finalized", res.FinalizedArchives, res.Finalized)
	}
	if _, err := os.Stat(filepath.Join(archives, mergeName)); !os.IsNotExist(err) {
		t.Fatalf("merge stat error = %v, want not exist", err)
	}
	loaded, err := loadRollupState(filepath.Join(archives, stateFileName))
	if err != nil {
		t.Fatalf("loadRollupState() error = %v", err)
	}
	if _, ok := loaded.Lineages["fb2"]; ok {
		t.Fatalf("state lineages = %#v, want fb2 removed", loaded.Lineages)
	}
	entries := logs.FilterMessage("Archive finalized").All()
	if len(entries) != 1 || entries[0].ContextMap()["policy"] != string(FinalizationPolicyRolling) ||
		entries[0].ContextMap()["reason"] != string(FinalizationReasonRollingDeadline) {
		t.Fatalf("logs = %#v, want rolling_deadline finalization reason", logs.All())
	}
}

func TestRunRollingRejectsInvalidDeadlineState(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mergeName := "fb2-0000000001-0000000001.merging"
	writeZip(t, filepath.Join(archives, mergeName), map[string]string{"1.fb2": "one"})
	state := &rollupState{Version: 1, Lineages: map[string]rollupLineageState{
		"fb2": {
			ActiveMerge: mergeName,
			FirstBook:   1,
			LastBook:    1,
			Policy:      FinalizationPolicyRolling,
			Rolling:     "14d",
			StartedAt:   timePtr(startedAt),
			Deadline:    timePtr(startedAt.Add(13 * 24 * time.Hour)),
		},
	}}
	if err := saveRollupState(filepath.Join(archives, stateFileName), state); err != nil {
		t.Fatalf("saveRollupState() error = %v", err)
	}

	_, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testRollingFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
	})
	if err == nil || !strings.Contains(err.Error(), "deadline does not match") {
		t.Fatalf("Run() error = %v, want invalid deadline error", err)
	}
}

func TestRunCalendarCreatesMergeState(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	writeZip(t, filepath.Join(updates, "f.fb2.000001-000001.zip"), map[string]string{"1.fb2": "one"})

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1),
		Finalization: FinalizationOptions{
			Policy:         FinalizationPolicyCalendar,
			CalendarBucket: CalendarBucketMonth,
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 0 || filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000001.merging" {
		t.Fatalf("result = %#v, want active calendar merge", res)
	}
	state, err := loadRollupState(filepath.Join(archives, stateFileName))
	if err != nil {
		t.Fatalf("loadRollupState() error = %v", err)
	}
	lineage := state.Lineages["fb2"]
	if lineage.Policy != FinalizationPolicyCalendar || lineage.Calendar != CalendarBucketMonth {
		t.Fatalf("lineage = %#v, want calendar month", lineage)
	}
	wantStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if lineage.BucketStart == nil || !lineage.BucketStart.Equal(wantStart) || lineage.BucketEnd == nil || !lineage.BucketEnd.Equal(wantEnd) {
		t.Fatalf("bucket = %v:%v, want %v:%v", lineage.BucketStart, lineage.BucketEnd, wantStart, wantEnd)
	}
}

func TestRunCalendarFinalizesExpiredMergeWithoutUpdates(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	bucketStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bucketEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mergeName := "fb2-0000000001-0000000001.merging"
	writeZip(t, filepath.Join(archives, mergeName), map[string]string{"1.fb2": "one"})
	state := &rollupState{Version: 1, Lineages: map[string]rollupLineageState{
		"fb2": {
			ActiveMerge: mergeName,
			FirstBook:   1,
			LastBook:    1,
			Policy:      FinalizationPolicyCalendar,
			Calendar:    CalendarBucketMonth,
			BucketStart: timePtr(bucketStart),
			BucketEnd:   timePtr(bucketEnd),
		},
	}}
	if err := saveRollupState(filepath.Join(archives, stateFileName), state); err != nil {
		t.Fatalf("saveRollupState() error = %v", err)
	}
	core, logs := observer.New(zap.InfoLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testCalendarFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
		Now:             func() time.Time { return bucketEnd },
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Finalized != 1 || filepath.Base(res.FinalizedArchives[0]) != "fb2-0000000001-0000000001.zip" {
		t.Fatalf("result = %#v, want expired calendar merge finalized", res)
	}
	entries := logs.FilterMessage("Archive finalized").All()
	if len(entries) != 1 || entries[0].ContextMap()["policy"] != string(FinalizationPolicyCalendar) ||
		entries[0].ContextMap()["reason"] != string(FinalizationReasonCalendarBucketEnd) {
		t.Fatalf("logs = %#v, want calendar_bucket_end finalization reason", logs.All())
	}
}

func TestRunCalendarRejectsInvalidBucketState(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	mergeName := "fb2-0000000001-0000000001.merging"
	writeZip(t, filepath.Join(archives, mergeName), map[string]string{"1.fb2": "one"})
	state := &rollupState{Version: 1, Lineages: map[string]rollupLineageState{
		"fb2": {
			ActiveMerge: mergeName,
			FirstBook:   1,
			LastBook:    1,
			Policy:      FinalizationPolicyCalendar,
			Calendar:    CalendarBucketMonth,
			BucketStart: timePtr(time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)),
			BucketEnd:   timePtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
		},
	}}
	if err := saveRollupState(filepath.Join(archives, stateFileName), state); err != nil {
		t.Fatalf("saveRollupState() error = %v", err)
	}

	_, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testCalendarFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
	})
	if err == nil || !strings.Contains(err.Error(), "bucket range is invalid") {
		t.Fatalf("Run() error = %v, want invalid bucket range error", err)
	}
}

func TestRunCalendarPreservesExistingBucketWhenMergeGrows(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	bucketStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bucketEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mergeName := "fb2-0000000001-0000000001.merging"
	writeZip(t, filepath.Join(archives, mergeName), map[string]string{"1.fb2": "one"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000002-0000000002.zip"), map[string]string{"2.fb2": "two"})
	state := &rollupState{Version: 1, Lineages: map[string]rollupLineageState{
		"fb2": {
			ActiveMerge: mergeName,
			FirstBook:   1,
			LastBook:    1,
			Policy:      FinalizationPolicyCalendar,
			Calendar:    CalendarBucketMonth,
			BucketStart: timePtr(bucketStart),
			BucketEnd:   timePtr(bucketEnd),
		},
	}}
	if err := saveRollupState(filepath.Join(archives, stateFileName), state); err != nil {
		t.Fatalf("saveRollupState() error = %v", err)
	}

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		Finalization:    testCalendarFinalization(),
		TargetSizeBytes: testTargetSizeBytes(1),
		Now:             func() time.Time { return time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q, want grown merge", res.ActiveMerge)
	}
	loaded, err := loadRollupState(filepath.Join(archives, stateFileName))
	if err != nil {
		t.Fatalf("loadRollupState() error = %v", err)
	}
	lineage := loaded.Lineages["fb2"]
	if lineage.ActiveMerge != "fb2-0000000001-0000000002.merging" || lineage.FirstBook != 1 || lineage.LastBook != 2 {
		t.Fatalf("lineage = %#v, want grown range", lineage)
	}
	if lineage.BucketStart == nil || !lineage.BucketStart.Equal(bucketStart) || lineage.BucketEnd == nil || !lineage.BucketEnd.Equal(bucketEnd) {
		t.Fatalf("lineage = %#v, want original bucket preserved", lineage)
	}
}

func TestRunRemovesSupersededLastArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	oldArchive := filepath.Join(archives, "fb2-0000000001-0000000001.zip")
	writeZip(t, oldArchive, map[string]string{"1.fb2": "one"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000002-0000000002.zip"), map[string]string{"2.fb2": "two"})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(oldArchive); !os.IsNotExist(err) {
		t.Fatalf("old archive stat error = %v, want not exist", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	entries, err := countZipEntries(res.ActiveMerge)
	if err != nil {
		t.Fatalf("countZipEntries() error = %v", err)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2", entries)
	}
}

func TestRunPreservesUpdateArchives(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	bogusUpdate := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip.tmp")
	writeZip(t, bogusUpdate, map[string]string{"1.fb2": "one"})
	validUpdate := filepath.Join(updates, "f.fb2.0000000002-0000000002.zip")
	writeZip(t, validUpdate, map[string]string{"2.fb2": "two"})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(bogusUpdate); err != nil {
		t.Fatalf("bogus update stat error = %v, want preserved", err)
	}
	if _, err := os.Stat(validUpdate); err != nil {
		t.Fatalf("valid update stat error = %v, want preserved", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000002-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
}

func TestRunKeepsNewEntriesFromOverlappingUpdates(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	oldArchive := filepath.Join(archives, "fb2-0000000001-0000000100.zip")
	writeZip(t, oldArchive, map[string]string{"1.fb2": "one", "100.fb2": "hundred"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000050-0000000102.zip"), map[string]string{
		"50.fb2":  "duplicate",
		"101.fb2": "new",
		"102.fb2": "new",
	})
	core, logs := observer.New(zap.WarnLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1_000_000),
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000102.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	if entries, err := countZipEntries(res.ActiveMerge); err != nil || entries != 4 {
		t.Fatalf("entries=%d err=%v, want 4 entries", entries, err)
	}
	if logs.FilterMessage("Overlapping archive update selected").Len() != 1 {
		t.Fatalf("logs = %#v, want overlapping update warning", logs.All())
	}
	if logs.FilterMessage("Skipping already finalized archive entry from overlapping update").Len() != 1 {
		t.Fatalf("logs = %#v, want duplicate entry warning", logs.All())
	}
}

func TestRunSkipsUpdateWhenFilenameEndExceedsActualEntries(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	mergePath := filepath.Join(archives, "fb2-0000000001-0000000101.merging")
	writeZip(t, mergePath, map[string]string{
		"1.fb2":   "one",
		"101.fb2": "one hundred one",
	})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000101-0000000102.zip"), map[string]string{
		"101.fb2": "duplicate",
	})
	core, logs := observer.New(zap.DebugLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1_000_000),
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Updates != 0 {
		t.Fatalf("Updates = %d, want 0", res.Updates)
	}
	if entries, err := countZipEntries(mergePath); err != nil || entries != 2 {
		t.Fatalf("merge entries=%d err=%v, want original two entries", entries, err)
	}
	if logs.FilterMessage("Archive update range adjusted to actual entries").Len() != 1 {
		t.Fatalf("logs = %#v, want actual range warning", logs.All())
	}
	if logs.FilterMessage("Skipping archive update with no new entries").Len() != 1 {
		t.Fatalf("logs = %#v, want no-new-entries warning", logs.All())
	}
	if logs.FilterMessage("Processing update archive").Len() != 0 {
		t.Fatalf("logs = %#v, want stale update skipped before processing", logs.All())
	}
}

func TestRunSkipsDuplicatesWithinActiveWorkArchive(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	oldArchive := filepath.Join(archives, "fb2-0000000001-0000000100.zip")
	writeZip(t, oldArchive, map[string]string{"1.fb2": "one", "100.fb2": "hundred"})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000101-0000000150.zip"), map[string]string{
		"101.fb2": "one hundred one",
		"120.fb2": "first copy",
		"150.fb2": "one hundred fifty",
	})
	writeZip(t, filepath.Join(updates, "f.fb2.0000000120-0000000160.zip"), map[string]string{
		"120.fb2": "second copy",
		"151.fb2": "one hundred fifty one",
		"160.fb2": "one hundred sixty",
	})
	core, logs := observer.New(zap.WarnLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1_000_000),
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000160.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
	counts := countZipEntryNames(t, res.ActiveMerge)
	if counts["120.fb2"] != 1 {
		t.Fatalf("120.fb2 count = %d, want 1", counts["120.fb2"])
	}
	if len(counts) != 7 {
		t.Fatalf("entry counts = %#v, want 7 unique entries", counts)
	}
	if logs.FilterMessage("Skipping duplicate archive entry from overlapping update").Len() != 1 {
		t.Fatalf("logs = %#v, want active-work duplicate warning", logs.All())
	}
}

func TestRunUsesMinMaxRangeForOutOfOrderEntries(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	writeZipOrdered(t, filepath.Join(updates, "f.fb2.0000000001-0000000002.zip"), []zipEntry{
		{Name: "2.fb2", Content: "two"},
		{Name: "1.fb2", Content: "one"},
	})

	res, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(res.ActiveMerge) != "fb2-0000000001-0000000002.merging" {
		t.Fatalf("ActiveMerge = %q", res.ActiveMerge)
	}
}

func TestRunFailsWhenUpdateCannotBeOpened(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	updatePath := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip")
	if err := os.WriteFile(updatePath, []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write update: %v", err)
	}

	_, err := Run(context.Background(), testOptions(archives, updates, 1_000_000))
	if err == nil || !strings.Contains(err.Error(), "open update archive") {
		t.Fatalf("Run() error = %v, want open update archive error", err)
	}
	if _, err := os.Stat(updatePath); err != nil {
		t.Fatalf("update stat error = %v, want preserved", err)
	}
}

func TestRunFailsWhenEntryCopyFailsAndKeepsCommittedOutput(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	firstUpdate := filepath.Join(updates, "f.fb2.0000000001-0000000001.zip")
	secondUpdate := filepath.Join(updates, "f.fb2.0000000002-0000000002.zip")
	writeZip(t, firstUpdate, map[string]string{"1.fb2": "one"})
	writeZip(t, secondUpdate, map[string]string{"2.fb2": "two"})
	copyErr := errors.New("copy failed")
	copies := 0

	_, err := run(
		context.Background(),
		testOptions(archives, updates, 1),
		func(writer *zip.Writer, file *zip.File) error {
			copies++
			if copies == 2 {
				return copyErr
			}
			return writer.Copy(file)
		},
	)
	if !errors.Is(err, copyErr) {
		t.Fatalf("run() error = %v, want copy failure", err)
	}
	for _, path := range []string{firstUpdate, secondUpdate} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("update %q stat error = %v, want preserved", path, err)
		}
	}
	outputs, err := filepath.Glob(filepath.Join(archives, "fb2-*.zip"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(outputs) != 1 {
		t.Fatalf("committed outputs = %#v, want one", outputs)
	}
	if entries, err := countZipEntries(outputs[0]); err != nil || entries != 1 {
		t.Fatalf("committed output entries=%d err=%v, want one", entries, err)
	}
}

func TestRunWarnsAndIgnoresEmptyAndNonNumericEntries(t *testing.T) {
	t.Parallel()

	archives := t.TempDir()
	updates := t.TempDir()
	updatePath := filepath.Join(updates, "f.fb2.0000000001-0000000003.zip")
	writeZipOrdered(t, updatePath, []zipEntry{
		{Name: "1.fb2", Content: "one"},
		{Name: "2.fb2"},
		{Name: "notes.txt", Content: "ignored"},
	})
	core, logs := observer.New(zap.WarnLevel)

	res, err := Run(context.Background(), Options{
		ArchiveDir:      archives,
		UpdateDirs:      []string{updates},
		TargetSizeBytes: testTargetSizeBytes(1_000_000),
		ValidateCRC:     true,
		Log:             zap.New(core),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if logs.FilterMessage("Skipping empty archive entry").Len() != 1 {
		t.Fatalf("empty entry warnings = %d, want one", logs.FilterMessage("Skipping empty archive entry").Len())
	}
	if logs.FilterMessage("Skipping entry with non-numeric name").Len() != 1 {
		t.Fatalf("non-numeric entry warnings = %d, want one", logs.FilterMessage("Skipping entry with non-numeric name").Len())
	}
	if entries, err := countZipEntries(res.ActiveMerge); err != nil || entries != 1 {
		t.Fatalf("active merge entries=%d err=%v, want one", entries, err)
	}
	if _, err := os.Stat(updatePath); err != nil {
		t.Fatalf("update stat error = %v, want preserved", err)
	}
}

func TestRunOptionalCRCValidation(t *testing.T) {
	t.Parallel()

	fastArchives := t.TempDir()
	fastUpdates := t.TempDir()
	fastUpdate := filepath.Join(fastUpdates, "f.fb2.0000000001-0000000001.zip")
	writeCorruptStoredZip(t, fastUpdate, "1.fb2", "one")
	sourceMethod, sourceRaw := readRawZipEntry(t, fastUpdate, "1.fb2")

	res, err := Run(context.Background(), testOptions(fastArchives, fastUpdates, 1_000_000))
	if err != nil {
		t.Fatalf("Run(validate_crc=false) error = %v", err)
	}
	outputMethod, outputRaw := readRawZipEntry(t, res.ActiveMerge, "1.fb2")
	if outputMethod != sourceMethod || !bytes.Equal(outputRaw, sourceRaw) {
		t.Fatalf("direct copy changed compressed entry: method=%d/%d bytes_equal=%v", outputMethod, sourceMethod, bytes.Equal(outputRaw, sourceRaw))
	}

	checkedArchives := t.TempDir()
	checkedUpdates := t.TempDir()
	checkedUpdate := filepath.Join(checkedUpdates, "f.fb2.0000000001-0000000001.zip")
	writeCorruptStoredZip(t, checkedUpdate, "1.fb2", "one")
	_, err = Run(context.Background(), Options{
		ArchiveDir:      checkedArchives,
		UpdateDirs:      []string{checkedUpdates},
		TargetSizeBytes: testTargetSizeBytes(1_000_000),
		ValidateCRC:     true,
	})
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("Run(validate_crc=true) error = %v, want zip.ErrChecksum", err)
	}
	if _, err := os.Stat(checkedUpdate); err != nil {
		t.Fatalf("checked update stat error = %v, want preserved", err)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	entries := make([]zipEntry, 0, len(files))
	for name, content := range files {
		entries = append(entries, zipEntry{Name: name, Content: content})
	}
	writeZipOrdered(t, path, entries)
}

func testOptions(archives string, updates string, size int64) Options {
	return Options{ArchiveDir: archives, UpdateDirs: []string{updates}, TargetSizeBytes: testTargetSizeBytes(size)}
}

func testTargetSizeBytes(size int64) map[string]int64 {
	return map[string]int64{"fb2": size, "usr": size}
}

func testRollingFinalization() FinalizationOptions {
	return FinalizationOptions{Policy: FinalizationPolicyRolling, RollingDuration: 14 * 24 * time.Hour, RollingText: "14d"}
}

func testCalendarFinalization() FinalizationOptions {
	return FinalizationOptions{Policy: FinalizationPolicyCalendar, CalendarBucket: CalendarBucketMonth}
}

func mustCompileDefaultUpdatePatterns(t *testing.T) []compiledUpdatePattern {
	t.Helper()
	patterns, err := compileUpdatePatterns(nil)
	if err != nil {
		t.Fatalf("compileUpdatePatterns() error = %v", err)
	}
	return patterns
}

func writeCorruptStoredZip(t *testing.T, path string, name string, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		t.Fatalf("create stored entry %s: %v", name, err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("write stored entry %s: %v", name, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip for corruption: %v", err)
	}
	offset, err := zr.File[0].DataOffset()
	if err != nil {
		zr.Close()
		t.Fatalf("entry data offset: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close zip reader: %v", err)
	}
	f, err = os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open zip for corruption: %v", err)
	}
	b := []byte{0}
	if _, err := f.ReadAt(b, offset); err != nil {
		f.Close()
		t.Fatalf("read entry byte: %v", err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b, offset); err != nil {
		f.Close()
		t.Fatalf("corrupt entry byte: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupted zip: %v", err)
	}
}

func readRawZipEntry(t *testing.T, path string, name string) (uint16, []byte) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.OpenRaw()
		if err != nil {
			t.Fatalf("open raw entry %s: %v", name, err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read raw entry %s: %v", name, err)
		}
		return file.Method, data
	}
	t.Fatalf("entry %s not found in %s", name, path)
	return 0, nil
}

func countZipEntryNames(t *testing.T, path string) map[string]int {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer zr.Close()
	counts := make(map[string]int, len(zr.File))
	for _, file := range zr.File {
		counts[file.Name]++
	}
	return counts
}

type zipEntry struct {
	Name    string
	Content string
}

func writeZipOrdered(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		w, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatalf("create entry %s: %v", entry.Name, err)
		}
		if _, err := w.Write([]byte(entry.Content)); err != nil {
			t.Fatalf("write entry %s: %v", entry.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}

type fakeInfo struct {
	name string
}

func (f fakeInfo) Name() string { return f.name }

func (f fakeInfo) Size() int64 { return 0 }

func (f fakeInfo) Mode() os.FileMode { return 0 }

func (f fakeInfo) ModTime() time.Time { return time.Time{} }

func (f fakeInfo) IsDir() bool { return false }

func (f fakeInfo) Sys() any { return nil }
