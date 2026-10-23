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
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
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

func (e *env) assertNoSecrets(t *testing.T, label string, withResults bool) {
	t.Helper()
	body := e.ag("")
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
	e.assertNoSecrets(t, "setup", false)
	e.ap("/greetings", yes)
	e.assertNoSecrets(t, "collecting", false)
	e.addItems(t)
	e.ap("/draw", url.Values{})
	e.assertNoSecrets(t, "drawn", true)
	e.ap("/results", yes)
	e.assertNoSecrets(t, "delivered", true)
	if len(e.fake.Sent) != 8 {
		t.Fatalf("sent %d, want 8", len(e.fake.Sent))
	}
	e.reconfigure(t, strings.Replace(fourYAML, "  - { name: \"Ola\", phone: \"+48600100400\" }\n", "", 1))
	e.assertNoSecrets(t, "change pending", true)
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
