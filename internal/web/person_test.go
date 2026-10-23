package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/locale"
)

func TestPersonPageRenders(t *testing.T) {
	e := newEnv(t, baseYAML)
	w := e.get("/p/" + e.tokens["Maciej"])
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Maciej") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `<html lang="en">`) {
		t.Fatal("html lang")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("referrer policy")
	}
	if w := e.get("/static/style.css"); w.Code != 200 {
		t.Fatalf("css %d", w.Code)
	}
}

func TestUnknownToken(t *testing.T) {
	e := newEnv(t, baseYAML)
	a := e.get("/p/AAAAAAAAAAAAAAAA")
	b := e.get("/p/" + e.admin)
	if a.Code != 404 || b.Code != 404 {
		t.Fatalf("%d %d", a.Code, b.Code)
	}
	if a.Body.String() != b.Body.String() || !strings.Contains(a.Body.String(), locale.T("en", "notfound.title", nil)) {
		t.Fatal("not the same notfound page")
	}
	if w := e.post("/p/NOPE/items", url.Values{"title": {"x"}}); w.Code != 404 {
		t.Fatalf("post %d", w.Code)
	}
}

func TestAddItemFlow(t *testing.T) {
	e := newEnv(t, baseYAML)
	p := "/p/" + e.tokens["Maciej"]
	w := e.post(p+"/items", url.Values{"title": {"Warm socks"}, "link": {"https://allegro.pl/x"}, "note": {"size 44"}})
	if w.Code != 303 || w.Header().Get("Location") != p {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
	body := e.get(p).Body.String()
	if !strings.Contains(body, "Warm socks") || !strings.Contains(body, "size 44") {
		t.Fatal(body)
	}
	v, _ := e.r.PersonView(context.Background(), e.tokens["Maciej"])
	id := v.Items[0].ID
	idp := p + "/items/" + itoa(id)
	if w := e.post(idp, url.Values{"title": {"Cold socks"}}); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if !strings.Contains(e.get(p).Body.String(), "Cold socks") {
		t.Fatal("update")
	}
	if w := e.post(idp+"/delete", nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if strings.Contains(e.get(p).Body.String(), "Cold socks") {
		t.Fatal("delete")
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func TestJavascriptLinkRejected(t *testing.T) {
	e := newEnv(t, baseYAML)
	p := "/p/" + e.tokens["Maciej"]
	w := e.post(p+"/items", url.Values{"title": {"Evil"}, "link": {"javascript:alert(1)"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), locale.T("en", "item.link_invalid", nil)) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	v, _ := e.r.PersonView(context.Background(), e.tokens["Maciej"])
	if len(v.Items) != 0 {
		t.Fatal("stored")
	}
}

func TestTitleEscaped(t *testing.T) {
	e := newEnv(t, baseYAML)
	p := "/p/" + e.tokens["Maciej"]
	e.post(p+"/items", url.Values{"title": {"<script>x</script>"}})
	body := e.get(p).Body.String()
	if strings.Contains(body, "<script>x") || !strings.Contains(body, "&lt;script&gt;x&lt;/script&gt;") {
		t.Fatal(body)
	}
}

func TestLangSwitch(t *testing.T) {
	e := newEnv(t, baseYAML)
	p := "/p/" + e.tokens["Maciej"]
	if !strings.Contains(e.get(p).Body.String(), locale.T("en", "person.add_item", nil)) {
		t.Fatal("default en")
	}
	w := e.get(p + "?lang=pl")
	if !strings.Contains(w.Header().Get("Set-Cookie"), "lang=pl") || !strings.Contains(w.Body.String(), locale.T("pl", "person.add_item", nil)) {
		t.Fatalf("%q", w.Header().Get("Set-Cookie"))
	}
	w = e.get(p + "?lang=xx")
	if w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Body.String(), locale.T("en", "person.add_item", nil)) {
		t.Fatal("xx should be ignored")
	}
}

func TestCookieLang(t *testing.T) {
	e := newEnv(t, baseYAML)
	req := httptest.NewRequest("GET", "/p/"+e.tokens["Maciej"], nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "pl"})
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `<html lang="pl">`) {
		t.Fatal("cookie lang ignored")
	}
}

// deliver runs greetings, draw and results so the letter card is shown.
func deliver(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	for name, tok := range e.tokens {
		if w := e.post("/p/"+tok+"/items", url.Values{"title": {"Gift for " + name}, "link": {"https://shop.test/" + name}, "note": {"note " + name}}); w.Code != 303 {
			t.Fatalf("%d", w.Code)
		}
	}
	if _, err := e.r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := e.r.Draw(ctx)
	if err != nil || len(out.Problems) > 0 {
		t.Fatalf("%v %v", err, out.Problems)
	}
	if _, err := e.r.SendResults(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenAfterDelivery(t *testing.T) {
	e := newEnv(t, baseYAML)
	deliver(t, e)
	p := "/p/" + e.tokens["Maciej"]
	w := e.post(p+"/items", url.Values{"title": {"More"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), locale.T("en", "person.frozen_error", nil)) {
		t.Fatalf("%d", w.Code)
	}
	body := e.get(p).Body.String()
	if strings.Contains(body, locale.T("en", "person.add_item", nil)) {
		t.Fatal("add form shown while frozen")
	}
}

func TestLetterToSanta(t *testing.T) {
	e := newEnv(t, baseYAML)
	deliver(t, e)
	v, _ := e.r.PersonView(context.Background(), e.tokens["Maciej"])
	if v.Receiver == nil {
		t.Fatal("no receiver")
	}
	body := e.get("/p/" + e.tokens["Maciej"]).Body.String()
	for _, want := range []string{v.Receiver.Name, "Gift for " + v.Receiver.Name, "note " + v.Receiver.Name,
		`href="https://shop.test/` + v.Receiver.Name + `"`, `rel="noopener noreferrer"`, `target="_blank"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestSwitcherWorksOnErrorPage(t *testing.T) {
	e := newEnv(t, baseYAML)
	p := "/p/" + e.tokens["Maciej"]
	w := e.post(p+"/items", url.Values{"title": {"x"}, "link": {"javascript:1"}})
	want := `href="` + p + `?lang=pl"`
	if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
		t.Fatalf("switcher link missing: %d", w.Code)
	}
	w = e.get(p + "?lang=pl")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<html lang="pl">`) {
		t.Fatal("follow failed")
	}
}

func TestStaticNoListing(t *testing.T) {
	e := newEnv(t, baseYAML)
	for _, p := range []string{"/static/", "/static/nope.css"} {
		if w := e.get(p); w.Code != 404 || !strings.Contains(w.Body.String(), locale.T("en", "notfound.title", nil)) {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}
