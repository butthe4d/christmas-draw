package sender

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDryRunLogAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dry.log")
	l := DryRunLog{Path: path}
	t1 := time.Date(2026, 12, 1, 20, 15, 3, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	if err := l.Write("greeting", []SMS{{To: "+48600100200", Text: "Hej Ania! https://x/p/XXXXXXXXXXXXXXXX"}, {To: "+48600100201", Text: "Hej Ola!"}}, t1); err != nil {
		t.Fatal(err)
	}
	if err := l.Write("reminder", []SMS{{To: "+48600100202", Text: "Hej Ewa!"}}, t2); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	h1 := "=== 2026-12-01 20:15:03 greeting (2 SMS) ==="
	h2 := "=== 2026-12-01 21:15:03 reminder (1 SMS) ==="
	i1, i2 := strings.Index(got, h1), strings.Index(got, h2)
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("headers missing or out of order:\n%s", got)
	}
	for _, want := range []string{
		"+48600100200 [1 seg] Hej Ania! https://x/p/XXXXXXXXXXXXXXXX",
		"+48600100201 [1 seg] Hej Ola!",
		"+48600100202 [1 seg] Hej Ewa!",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing line %q in:\n%s", want, got)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestFakeFailTo(t *testing.T) {
	f := &Fake{FailTo: map[string]bool{"+48600100999": true}}
	ctx := context.Background()
	if _, err := f.Send(ctx, SMS{To: "+48600100999", Text: "x"}); err == nil {
		t.Fatal("expected error for FailTo number")
	}
	r1, err := f.Send(ctx, SMS{To: "+48600100200", Text: "a"})
	if err != nil || r1.ProviderID != 1 {
		t.Fatalf("r1 = %+v, %v", r1, err)
	}
	r2, err := f.Send(ctx, SMS{To: "+48600100201", Text: "b"})
	if err != nil || r2.ProviderID != 2 {
		t.Fatalf("r2 = %+v, %v", r2, err)
	}
	if len(f.Sent) != 2 || f.Sent[0].To != "+48600100200" || f.Sent[1].To != "+48600100201" {
		t.Fatalf("Sent = %+v", f.Sent)
	}
}

func TestFakePriceAndStatus(t *testing.T) {
	f := &Fake{Statuses: map[int64]string{1: "DELIVERED"}}
	ctx := context.Background()
	p, err := f.Price(ctx, []SMS{{Text: "a"}, {Text: strings.Repeat("a", 161)}})
	if err != nil || p < 0.419 || p > 0.421 {
		t.Fatalf("price = %v, %v; want 0.42", p, err)
	}
	if p, err := f.Price(ctx, nil); err != nil || p != 0 {
		t.Fatalf("empty price = %v, %v", p, err)
	}
	st, err := f.Status(ctx, []int64{1, 2})
	if err != nil || st[1] != "DELIVERED" || len(st) != 1 {
		t.Fatalf("status = %v, %v", st, err)
	}
	if f.StatusCalls != 1 {
		t.Fatalf("StatusCalls = %d", f.StatusCalls)
	}
}
