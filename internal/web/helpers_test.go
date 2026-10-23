package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const baseYAML = `base_url: https://x.test
greeting: "Hi {name}, write your wishes: {link}"
default_country_code: "+48"
dry_run: false
sms:
  token: tok
people:
  - { name: "Maciej", phone: "+48600100100" }
  - { name: "Ania", phone: "+48600100200" }
  - { name: "Tomek", phone: "+48600100300" }
`

type env struct {
	h      http.Handler
	r      *round.Round
	st     *store.Store
	tokens map[string]string // name -> token
	admin  string
	fake   *sender.Fake
	dry    string
}

func newEnv(t *testing.T, yaml string) *env {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &sender.Fake{}
	dry := filepath.Join(dir, "dry.log")
	r := round.New(cfg, st, fake, sender.DryRunLog{Path: dry}, round.Options{})
	ctx := context.Background()
	if _, err := r.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := r.AdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	people, err := st.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, p := range people {
		tokens[p.Name] = p.Token
	}
	return &env{h: NewServer(r, cfg), r: r, st: st, tokens: tokens, admin: admin, fake: fake, dry: dry}
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func (e *env) post(path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}
