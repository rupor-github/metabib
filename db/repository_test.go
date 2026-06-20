package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestInClauseAndInQuery(t *testing.T) {
	t.Parallel()

	clause, args := inClause([]int64{3, 5, 8})
	if clause != "?,?,?" {
		t.Fatalf("inClause clause = %q", clause)
	}
	if len(args) != 3 || args[0] != int64(3) || args[2] != int64(8) {
		t.Fatalf("inClause args = %#v", args)
	}

	query, args := inQuery("SELECT * FROM t WHERE id IN (", []int64{1, 2}, ") ORDER BY id")
	if query != "SELECT * FROM t WHERE id IN (?,?) ORDER BY id" {
		t.Fatalf("inQuery query = %q", query)
	}
	if len(args) != 2 {
		t.Fatalf("inQuery args = %#v", args)
	}
}

func TestFormatTime(t *testing.T) {
	t.Parallel()

	if got := formatTime(sql.NullTime{}); got != "" {
		t.Fatalf("formatTime(invalid) = %q", got)
	}
	tm := time.Date(2026, 6, 20, 12, 30, 45, 0, time.UTC)
	if got := formatTime(sql.NullTime{Time: tm, Valid: true}); got != "2026-06-20T12:30:45Z" {
		t.Fatalf("formatTime(valid) = %q", got)
	}
}

func TestFileIdentityFallback(t *testing.T) {
	t.Parallel()

	name := "123.fb2"
	stem := strings.TrimSuffix(name, ".fb2")
	if stem != "123" {
		t.Fatalf("stem = %q", stem)
	}
}
