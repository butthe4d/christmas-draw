package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const fourYAML = `base_url: https://x.test
greeting: "Hi {name}, write your wishes: {link}"
default_country_code: "+48"
dry_run: false
sms:
  token: tok
people:
  - { name: "Maciej", phone: "+48600100100" }
  - { name: "Ania", phone: "+48600100200" }
  - { name: "Tomek", phone: "+48600100300" }
  - { name: "Ola", phone: "+48600100400" }
`

var yes = url.Values{"confirm": {"yes"}}

func (e *env) ap(path string, form url.Values) *httptest.ResponseRecorder {
	return e.post("/admin/"+e.admin+path, form)
}

func (e *env) ag(query string) string {
	w := e.get("/admin/" + e.admin + query)
	return w.Body.String()
}

func (e *env) addItems(t *testing.T) {
	t.Helper()
	for name, tok := range e.tokens {
		w := e.post("/p/"+tok+"/items", url.Values{"title": {"Gift for " + name}})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("add item for %s: %d", name, w.Code)
		}
	}
}

// reconfigure restarts the app on the same database with a new config.
func (e *env) reconfigure(t *testing.T, yaml string) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	r := round.New(cfg, e.st, e.fake, sender.DryRunLog{Path: filepath.Join(t.TempDir(), "dry.log")}, round.Options{})
	if _, err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.r = r
	e.h = NewServer(r, cfg)
}

// expectedResults builds the result SMS texts the way round does, from the
// stored pairs and items, using the config's template and each giver's token.
func (e *env) expectedResults(t *testing.T, yaml string) []string {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	people, err := e.st.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]store.Person{}
	for _, p := range people {
		byID[p.ID] = p
	}
	pairs, err := e.st.Pairs(ctx)
	if err != nil || len(pairs) == 0 {
		t.Fatalf("pairs: %v (%d)", err, len(pairs))
	}
	var out []string
	for g, rc := range pairs {
		items, err := e.st.Items(ctx, rc)
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, it := range items {
			titles = append(titles, it.Title)
		}
		for _, tok := range []string{byID[g].Token, message.MaskedToken} {
			txt, err := message.Result(cfg.ResultTemplate, byID[g].Name, byID[rc].Name, message.Link(cfg.BaseURL, tok), titles, cfg.MaxResultSegments)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, txt)
		}
	}
	return out
}

func (e *env) assertNoSecrets(t *testing.T, label, yaml string, withResults bool) {
	t.Helper()
	body := e.ag("")
	if withResults {
		for _, txt := range e.expectedResults(t, yaml) {
			if strings.Contains(html.UnescapeString(body), txt) {
				t.Errorf("%s: admin page contains result SMS %q", label, txt)
			}
		}
		// the markup unseal.html uses for a pair, and its arrow
		if strings.Contains(body, `class="pair"`) || strings.Contains(body, "&rarr;") || strings.Contains(body, "\u2192") {
			t.Errorf("%s: admin page shows pairs before unseal", label)
		}
	}
	for _, tok := range e.tokens {
		if strings.Contains(body, tok) {
			t.Errorf("%s: admin page leaks a person token", label)
		}
	}
	if withResults {
		for _, m := range e.fake.Sent {
			if strings.Contains(body, m.Text) {
				t.Errorf("%s: admin page contains SMS text %q", label, m.Text)
			}
		}
	}
}

func TestAdminNoTokensLeak(t *testing.T) {
	e := newEnv(t, fourYAML)
	e.assertNoSecrets(t, "setup", fourYAML, false)
	e.ap("/greetings", yes)
	e.assertNoSecrets(t, "collecting", fourYAML, false)
	e.addItems(t)
	e.ap("/draw", url.Values{})
	e.assertNoSecrets(t, "drawn", fourYAML, true)
	e.ap("/results", yes)
	e.assertNoSecrets(t, "delivered", fourYAML, true)
	if len(e.fake.Sent) != 8 {
		t.Fatalf("sent %d, want 8", len(e.fake.Sent))
	}
	e.reconfigure(t, strings.Replace(fourYAML, "  - { name: \"Ola\", phone: \"+48600100400\" }\n", "", 1))
	e.assertNoSecrets(t, "change pending", fourYAML, true)
	if st, _ := e.r.State(context.Background()); st != round.ChangePending {
		t.Fatalf("state %s", st)
	}
}

func TestAdminConfirmRequired(t *testing.T) {
	e := newEnv(t, baseYAML)
	w := e.ap("/greetings", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	if len(e.fake.Sent) != 0 {
		t.Fatalf("sent %d without confirm", len(e.fake.Sent))
	}
	if !strings.Contains(w.Body.String(), "please confirm") {
		t.Error("no confirm prompt")
	}
}

func TestAdminFlow(t *testing.T) {
	e := newEnv(t, baseYAML)
	w := e.ap("/greetings", yes)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/admin/"+e.admin+"?msg=admin.flash.greetings_sent&n=3") {
		t.Fatalf("greetings: %d %q", w.Code, w.Header().Get("Location"))
	}
	if !strings.Contains(e.ag(w.Header().Get("Location")[len("/admin/"+e.admin):]), "Greetings sent: 3.") {
		t.Error("flash not shown")
	}
	e.addItems(t)
	if w := e.ap("/draw", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("draw: %d", w.Code)
	}
	if st, _ := e.r.State(context.Background()); st != round.Drawn {
		t.Fatalf("state %s", st)
	}
	if w := e.ap("/results", yes); w.Code != http.StatusSeeOther {
		t.Fatalf("results: %d", w.Code)
	}
	if st, _ := e.r.State(context.Background()); st != round.Delivered {
		t.Fatalf("state %s", st)
	}
	if n := len(e.fake.Sent); n != 6 {
		t.Fatalf("sent %d, want 3 greetings + 3 results", n)
	}
	// a second press gets a wrong-state flash, nothing more is sent
	w = e.ap("/results", yes)
	if !strings.Contains(w.Header().Get("Location"), "admin.flash.wrong_state") || len(e.fake.Sent) != 6 {
		t.Errorf("second results: %q, sent %d", w.Header().Get("Location"), len(e.fake.Sent))
	}
}

func TestAdminFlashOnlyKnownKeys(t *testing.T) {
	e := newEnv(t, baseYAML)
	body := e.ag("?msg=%3Cb%3Eevil%3C/b%3E&n=3&failed=Mallory")
	if strings.Contains(body, "evil") || strings.Contains(body, "Mallory") {
		t.Error("arbitrary flash text echoed")
	}
}

func TestAdminUnsealPage(t *testing.T) {
	e := newEnv(t, baseYAML)
	e.ap("/greetings", yes)
	e.addItems(t)
	e.ap("/draw", url.Values{})
	if w := e.ap("/unseal", url.Values{}); strings.Contains(w.Body.String(), `class="pair"`) {
		t.Fatal("unsealed without confirm")
	}
	w := e.ap("/unseal", yes)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unseal: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if n := strings.Count(w.Body.String(), `class="pair"`); n != 3 {
		t.Fatalf("pairs shown: %d", n)
	}
	body := e.ag("")
	if !strings.Contains(body, "esults were viewed on") {
		t.Error("unsealed notice missing")
	}
	if strings.Count(body, `class="pair"`) != 0 {
		t.Error("pairs on admin page")
	}
}

func TestAdminChangeFlow(t *testing.T) {
	e := newEnv(t, fourYAML)
	e.ap("/greetings", yes)
	e.addItems(t)
	e.ap("/draw", url.Values{})
	e.ap("/results", yes)
	e.reconfigure(t, strings.Replace(fourYAML, "  - { name: \"Ola\", phone: \"+48600100400\" }\n", "", 1))
	body := e.ag("")
	if !strings.Contains(body, "Ola") || !strings.Contains(body, "<textarea") {
		t.Fatal("removed name or textarea missing")
	}
	sent := len(e.fake.Sent)
	w := e.ap("/change", url.Values{"action": {"preview"}, "text": {"Plan changed, new draw soon."}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Plan changed, new draw soon.") {
		t.Fatalf("preview: %d", w.Code)
	}
	if len(e.fake.Sent) != sent {
		t.Fatal("preview sent SMS")
	}
	w = e.ap("/change", url.Values{"action": {"send"}, "text": {"Plan changed, new draw soon."}, "confirm": {"yes"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("send: %d", w.Code)
	}
	if st, _ := e.r.State(context.Background()); st != round.Collecting {
		t.Fatalf("state %s", st)
	}
	if len(e.fake.Sent) != sent+3 {
		t.Errorf("emergency SMS: %d", len(e.fake.Sent)-sent)
	}
}

func TestAdminSMSTest(t *testing.T) {
	e := newEnv(t, strings.Replace(baseYAML, "dry_run: false", "dry_run: false\norganiser_phone: \"+48600999999\"", 1))
	w := e.ap("/test-sms", yes)
	if w.Code != http.StatusOK || !strings.Contains(html.UnescapeString(w.Body.String()), "+48600999999") || len(e.fake.Sent) != 1 {
		t.Fatalf("test sms: %d sent %d", w.Code, len(e.fake.Sent))
	}
}

func TestAdminDryRunBanner(t *testing.T) {
	e := newEnv(t, strings.Replace(baseYAML, "dry_run: false", "dry_run: true", 1))
	body := e.ag("")
	if !strings.Contains(body, "Dry run") || !strings.Contains(body, e.dry) {
		t.Error("banner or log path missing")
	}
}

func TestAdminBadToken(t *testing.T) {
	e := newEnv(t, baseYAML)
	for _, p := range []string{"/admin/" + e.tokens["Ania"], "/admin/nope"} {
		if w := e.get(p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
	if w := e.post("/admin/"+e.tokens["Ania"]+"/greetings", yes); w.Code != http.StatusNotFound || len(e.fake.Sent) != 0 {
		t.Errorf("POST bad token: %d", w.Code)
	}
}

func TestAdminConfirmRequiredResultsUnsealRedraw(t *testing.T) {
	e := newEnv(t, baseYAML)
	e.ap("/greetings", yes)
	e.addItems(t)
	e.ap("/draw", url.Values{})
	before := len(e.fake.Sent)
	pairs, _ := e.st.Pairs(context.Background())

	w := e.ap("/results", url.Values{})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "please confirm") || len(e.fake.Sent) != before {
		t.Errorf("results without confirm: %d, sent %d", w.Code, len(e.fake.Sent)-before)
	}
	w = e.ap("/unseal", url.Values{})
	if !strings.Contains(w.Body.String(), "please confirm") || strings.Contains(w.Body.String(), `class="pair"`) {
		t.Error("unseal without confirm")
	}
	if at, _, _ := e.st.Meta(context.Background(), "unsealed_at"); at != "" {
		t.Error("unseal recorded without confirm")
	}
	// redrawing in Drawn needs confirmation too
	w = e.ap("/draw", url.Values{})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "please confirm") {
		t.Errorf("redraw without confirm: %d", w.Code)
	}
	after, _ := e.st.Pairs(context.Background())
	if len(after) != len(pairs) {
		t.Fatal("pairs changed")
	}
	for g, rc := range pairs {
		if after[g] != rc {
			t.Error("draw replaced without confirm")
		}
	}
	if w := e.ap("/draw", yes); w.Code != http.StatusSeeOther {
		t.Errorf("redraw with confirm: %d", w.Code)
	}
}

func TestAdminFailedNamesWithCommas(t *testing.T) {
	yaml := strings.Replace(baseYAML, `"Tomek"`, `"Tomek, Jr"`, 1)
	e := newEnv(t, yaml)
	e.fake.FailTo = map[string]bool{"+48600100300": true}
	w := e.ap("/greetings", yes)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query()["failed"]; len(got) != 1 || got[0] != "Tomek, Jr" {
		t.Fatalf("failed params = %q", got)
	}
	body := e.ag(strings.TrimPrefix(loc.String(), "/admin/"+e.admin))
	i := strings.Index(body, `class="flash"`)
	if i < 0 {
		t.Fatal("no flash shown")
	}
	flash := body[i : i+strings.Index(body[i:], "</p>")]
	if !strings.Contains(flash, "Tomek, Jr") {
		t.Errorf("flash lacks full failed name: %q", flash)
	}
}
