package round

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

type testPerson struct{ Name, Phone string }

var (
	pMaciej = testPerson{"Maciej", "+48600100100"}
	pAnia   = testPerson{"Ania", "+48600100200"}
	pTomek  = testPerson{"Tomek", "+48600100300"}
	pOla    = testPerson{"Ola", "+48600100400"}
)

// testYAML builds a config: extra (top-level YAML lines, e.g. "couples: ...")
// is appended to the defaults, followed by the people list.
func testYAML(extra string, people ...testPerson) string {
	var b strings.Builder
	b.WriteString("base_url: https://x.test\n")
	b.WriteString("greeting: \"Hi {name}, write your wishes: {link}\"\n")
	b.WriteString("default_country_code: \"+48\"\n")
	b.WriteString("dry_run: false\n")
	b.WriteString("sms:\n  token: tok\n")
	b.WriteString(extra)
	b.WriteString("people:\n")
	for _, p := range people {
		b.WriteString("  - { name: \"" + p.Name + "\", phone: \"" + p.Phone + "\" }\n")
	}
	return b.String()
}

// defaultYAML is the standard 3-person config: Maciej, Ania, Tomek.
var defaultYAML = testYAML("", pMaciej, pAnia, pTomek)

// fixedNow is the clock used by newTestRound.
var fixedNow = time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)

func newTestRound(t *testing.T, yaml string) (*Round, *store.Store, *sender.Fake, string) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &sender.Fake{}
	dryPath := filepath.Join(dir, "dry.log")
	r := New(cfg, st, fake, sender.DryRunLog{Path: dryPath}, Options{Now: func() time.Time { return fixedNow }})
	return r, st, fake, dryPath
}

// reconfigure swaps in a new config on the same DB (simulates an edit + restart).
func reconfigure(t *testing.T, r *Round, yaml string) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	r.cfg = cfg
}

func setState(t *testing.T, st *store.Store, s State) {
	t.Helper()
	if err := st.SetMeta(context.Background(), metaState, string(s)); err != nil {
		t.Fatal(err)
	}
}

func mustSync(t *testing.T, r *Round) SyncReport {
	t.Helper()
	rep, err := r.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return rep
}
