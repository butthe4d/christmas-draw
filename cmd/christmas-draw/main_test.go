package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const baseCfg = `base_url: https://x.test
greeting: "Hi {name}, wishes: {link}"
default_country_code: "+48"
dry_run: true
sms: { token: tok }
`

const peopleCfg = `people:
  - { name: Maciej, phone: "600100200" }
  - { name: Ania, phone: "600100201" }
  - { name: Tomek, phone: "600100202" }
`

// infeasible: Ania may not draw anyone.
const infeasibleCfg = peopleCfg + `exclusions:
  - [Ania, Maciej]
  - [Ania, Tomek]
`

func setup(t *testing.T, cfg string) (cfgPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, filepath.Join(dir, "test.db")
}

func exec(args []string, stdin string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestCheckOK(t *testing.T) {
	cfg, db := setup(t, baseCfg+peopleCfg)
	code, out, _ := exec([]string{"check", "-config", cfg, "-db", db}, "")
	if code != 0 || !strings.HasSuffix(strings.TrimSpace(out), "OK") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, want := range []string{"Maciej: +48600100200", "Ania: +48600100201", "Tomek: +48600100202"} {
		if !strings.Contains(out, want) {
			t.Errorf("check output lacks %q: %q", want, out)
		}
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("check must not create the DB file, stat err=%v", err)
	}
}

func TestCheckProblems(t *testing.T) {
	cfg, db := setup(t, baseCfg+infeasibleCfg)
	want := locale.M("draw.nobody_allowed", "name", "Ania")
	code, out, _ := exec([]string{"check", "-config", cfg, "-db", db}, "")
	if code != 1 || !strings.Contains(out, want.In("en")) {
		t.Fatalf("code=%d out=%q want %q", code, out, want.In("en"))
	}
	cfg, db = setup(t, baseCfg+"language: pl\n"+infeasibleCfg)
	code, out, _ = exec([]string{"check", "-config", cfg, "-db", db}, "")
	if code != 1 || !strings.Contains(out, want.In("pl")) || want.In("pl") == want.In("en") {
		t.Fatalf("code=%d out=%q want %q", code, out, want.In("pl"))
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("check must not create the DB file")
	}
}

func TestCheckUnparsableConfigIsEnglish(t *testing.T) {
	cfg, db := setup(t, "language: pl\n: : bad")
	code, out, _ := exec([]string{"check", "-config", cfg, "-db", db}, "")
	if code != 1 || !strings.Contains(out, "Invalid config file") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCheckGreetingTooLong(t *testing.T) {
	long := strings.Repeat("x", 400)
	cfg, db := setup(t, strings.Replace(baseCfg, "Hi {name}, wishes: {link}", long+" {link}", 1)+peopleCfg)
	code, out, _ := exec([]string{"check", "-config", cfg, "-db", db}, "")
	if code != 1 || !strings.Contains(out, "greeting") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func seed(t *testing.T, cfgPath, dbPath string) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := round.New(cfg, st, &sender.Fake{}, sender.DryRunLog{Path: filepath.Join(filepath.Dir(dbPath), "dry.log")}, round.Options{})
	rep, err := r.Sync(context.Background())
	if err != nil || !rep.AdminTokenCreated {
		t.Fatalf("sync: %v %+v", err, rep)
	}
}

func countPeople(t *testing.T, dbPath string) int {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, err := st.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(ps)
}

func TestResetNeedsConfirm(t *testing.T) {
	cfg, db := setup(t, baseCfg+peopleCfg)
	seed(t, cfg, db)
	args := []string{"reset", "-config", cfg, "-db", db}

	code, out, _ := exec(args, "no\n")
	if code == 0 || countPeople(t, db) != 3 {
		t.Fatalf("declined reset changed state: code=%d people=%d", code, countPeople(t, db))
	}
	if !strings.Contains(out, "reset") {
		t.Fatalf("prompt missing: %q", out)
	}
	if code, _, _ = exec(args, "reset\n"); code != 0 || countPeople(t, db) != 0 {
		t.Fatalf("confirmed reset: code=%d people=%d", code, countPeople(t, db))
	}
}

func TestResetYesSkipsPrompt(t *testing.T) {
	cfg, db := setup(t, baseCfg+peopleCfg)
	seed(t, cfg, db)
	if code, _, _ := exec([]string{"reset", "-yes", "-config", cfg, "-db", db}, ""); code != 0 || countPeople(t, db) != 0 {
		t.Fatalf("code=%d people=%d", code, countPeople(t, db))
	}
}

func TestAdminLink(t *testing.T) {
	cfg, db := setup(t, baseCfg+peopleCfg)
	args := []string{"admin-link", "-config", cfg, "-db", db}
	if code, _, _ := exec(args, ""); code != 1 {
		t.Fatalf("before first start: code=%d", code)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("admin-link must not create the DB file")
	}
	seed(t, cfg, db)
	code, out, _ := exec(args, "")
	re := regexp.MustCompile(`^Admin page: https://x\.test/admin/[A-Z2-7]{16}\n$`)
	if code != 0 || !re.MatchString(out) {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	if code, _, errOut := exec([]string{"bogus"}, ""); code != 2 || !strings.Contains(errOut, "bogus") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestResetRemovesDryRunLog(t *testing.T) {
	cfg, db := setup(t, baseCfg+peopleCfg)
	seed(t, cfg, db)
	log := filepath.Join(filepath.Dir(db), "dry-run.log")
	if err := os.WriteFile(log, []byte("pairs?"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"reset", "-config", cfg, "-db", db}
	if code, _, _ := exec(args, "no\n"); code == 0 {
		t.Fatal("declined reset succeeded")
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatal("declined reset removed the dry-run log")
	}
	if code, _, _ := exec(args, "reset\n"); code != 0 {
		t.Fatal("reset failed")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("dry-run log still there: %v", err)
	}
	// No log at all is fine.
	if code, _, _ := exec(append(args, "-yes"), ""); code != 0 {
		t.Fatal("reset without a log failed")
	}
}
