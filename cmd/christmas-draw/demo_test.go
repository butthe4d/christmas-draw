package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	christmasdraw "github.com/butthe4d/christmas-draw"
	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/round"
)

func TestExampleConfigShape(t *testing.T) {
	cfg, err := config.Parse(christmasdraw.ExampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.People) != 7 || len(cfg.Couples) != 2 || len(cfg.Exclusions) != 1 {
		t.Fatalf("people=%d couples=%d exclusions=%d, want 7/2/1", len(cfg.People), len(cfg.Couples), len(cfg.Exclusions))
	}
	if !cfg.DryRun {
		t.Fatal("example config must default to dry_run")
	}
	names := make([]string, len(cfg.People))
	for i, p := range cfg.People {
		names[i] = p.Name
		if len(demoWishes[p.Name]) == 0 {
			t.Errorf("no demo wishes for %s", p.Name)
		}
	}
	if err := draw.Feasible(draw.NewRules(names, cfg.Couples, cfg.Exclusions, cfg.ExcludeCouples)); err != nil {
		t.Fatalf("example config has no valid draw: %v", err)
	}
}

func TestDemoBaseURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://localhost:8080",
		"0.0.0.0:9000":   "http://localhost:9000",
		"127.0.0.1:8081": "http://127.0.0.1:8081",
	}
	for in, want := range cases {
		got, err := demoBaseURL(in)
		if err != nil || got != want {
			t.Errorf("demoBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func seedForTest(t *testing.T, stage string) (*round.Round, string) {
	t.Helper()
	dir := t.TempDir()
	r, _, st, err := seedDemo(context.Background(), dir, stage, ":8099")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return r, dir
}

func TestDemoCollecting(t *testing.T) {
	r, dir := seedForTest(t, stageCollecting)
	ctx := context.Background()
	ov, err := r.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.State != round.Collecting || !ov.DryRun {
		t.Fatalf("state=%s dryRun=%v, want COLLECTING, dry-run", ov.State, ov.DryRun)
	}
	if ov.Ungreeted != 0 || ov.Infeasible != nil {
		t.Fatalf("ungreeted=%d infeasible=%v", ov.Ungreeted, ov.Infeasible)
	}
	for _, p := range ov.People {
		if p.Items == 0 {
			t.Errorf("%s has an empty list", p.Name)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "config.yaml")); err != nil || !bytes.Equal(b, christmasdraw.ExampleConfig) {
		t.Fatalf("config.yaml not written from the example: %v", err)
	}
	// The demo serves in dry-run: drawing writes the log and keeps the state.
	if _, err := r.Draw(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.State(ctx); s != round.Collecting {
		t.Fatalf("dry-run draw changed state to %s", s)
	}
}

func TestDemoDelivered(t *testing.T) {
	r, _ := seedForTest(t, stageDelivered)
	ctx := context.Background()
	if s, _ := r.State(ctx); s != round.Delivered {
		t.Fatalf("state=%s, want DELIVERED", s)
	}
	pairs, err := r.Unseal(ctx)
	if err != nil || len(pairs) != 7 {
		t.Fatalf("pairs=%d err=%v, want 7", len(pairs), err)
	}
}

func TestDemoRecreatesFromScratch(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, _, st, err := seedDemo(ctx, dir, stageDelivered, ":8099")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _, st, err := seedDemo(ctx, dir, stageCollecting, ":8099")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if s, _ := r.State(ctx); s != round.Collecting {
		t.Fatalf("second run state=%s, want COLLECTING", s)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("demo removed a file that is not its own")
	}
}

func TestDemoBadStage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"demo", "-stage", "drawn", "-dir", t.TempDir()}, nil, &out, &errb); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errb.String(), "drawn") {
		t.Fatalf("stderr=%q", errb.String())
	}
}
