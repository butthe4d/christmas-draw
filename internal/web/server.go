// Package web is a thin server-rendered layer over the round package.
// It never logs request paths: they contain secret tokens.
package web

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type server struct {
	r    *round.Round
	cfg  *config.Config
	tpls map[string]*template.Template // page name -> layout + page
}

// NewServer returns the HTTP handler for all pages.
func NewServer(r *round.Round, cfg *config.Config) http.Handler {
	s := &server{r: r, cfg: cfg, tpls: map[string]*template.Template{}}
	noT := func(string, ...string) string { return "" }
	for _, page := range []string{"person", "notfound", "error", "admin", "unseal"} {
		s.tpls[page] = template.Must(template.New("").Funcs(template.FuncMap{"t": noT}).
			ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html"))
	}
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /p/{token}", s.personPage)
	mux.HandleFunc("POST /p/{token}/items", s.personAdd)
	mux.HandleFunc("POST /p/{token}/items/{id}", s.personUpdate)
	mux.HandleFunc("POST /p/{token}/items/{id}/delete", s.personDelete)
	mux.HandleFunc("GET /admin/{token}", s.admin(s.adminPage))
	mux.HandleFunc("POST /admin/{token}/greetings", s.admin(s.adminGreetings))
	mux.HandleFunc("POST /admin/{token}/reminders", s.admin(s.adminReminders))
	mux.HandleFunc("POST /admin/{token}/people/{id}/greeting", s.admin(s.adminResendGreeting))
	mux.HandleFunc("POST /admin/{token}/test-sms", s.admin(s.adminTestSMS))
	mux.HandleFunc("POST /admin/{token}/draw", s.admin(s.adminDraw))
	mux.HandleFunc("POST /admin/{token}/results", s.admin(s.adminResults))
	mux.HandleFunc("POST /admin/{token}/people/{id}/result", s.admin(s.adminResendResult))
	mux.HandleFunc("POST /admin/{token}/unseal", s.admin(s.adminUnseal))
	mux.HandleFunc("POST /admin/{token}/change", s.admin(s.adminChange))
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/static/")
		if info, err := fs.Stat(static, name); err != nil || info.IsDir() {
			s.notFound(w, req)
			return
		}
		files.ServeHTTP(w, req)
	})
	mux.HandleFunc("/", s.notFound)
	return secure(mux)
}

// secure keeps secret links out of Referer headers and caches.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, req)
	})
}

type langOpt struct {
	Code, Name string
	Current    bool
}

// base is embedded in every page's data.
type base struct {
	Lang  string
	Path  string // canonical GET path of this page; the switcher links to Path?lang=xx
	Langs []langOpt
}

// visitorLang resolves ?lang= (sets the cookie), then the cookie, then the config default.
func (s *server) visitorLang(w http.ResponseWriter, req *http.Request) string {
	if l := req.URL.Query().Get("lang"); locale.Has(l) {
		http.SetCookie(w, &http.Cookie{
			Name: "lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600,
			Expires:  time.Now().Add(365 * 24 * time.Hour),
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		return l
	}
	if c, err := req.Cookie("lang"); err == nil && locale.Has(c.Value) {
		return c.Value
	}
	return s.cfg.Language
}

func (s *server) newBase(lang, path string) base {
	b := base{Lang: lang, Path: path}
	for _, c := range locale.Available() {
		b.Langs = append(b.Langs, langOpt{Code: c, Name: locale.T(c, "lang.name", nil), Current: c == lang})
	}
	return b
}

// render executes the page into a buffer first so a template error never
// produces a half-written response.
func (s *server) render(w http.ResponseWriter, status int, page, lang string, data any) {
	t, err := s.tpls[page].Clone()
	if err != nil {
		s.fail(w, lang, err)
		return
	}
	t = t.Funcs(template.FuncMap{"t": func(key string, kv ...string) string {
		var args map[string]string
		if len(kv) > 1 {
			args = map[string]string{}
			for i := 0; i+1 < len(kv); i += 2 {
				args[kv[i]] = kv[i+1]
			}
		}
		return locale.T(lang, key, args)
	}})
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.fail(w, lang, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// fail logs the error (never the request path or token) and shows a
// localized generic error page.
func (s *server) fail(w http.ResponseWriter, lang string, err error) {
	slog.Error("request failed", "err", err)
	t, terr := s.tpls["error"].Clone()
	if terr == nil {
		t = t.Funcs(template.FuncMap{"t": func(key string, _ ...string) string { return locale.T(lang, key, nil) }})
		var buf bytes.Buffer
		if terr = t.ExecuteTemplate(&buf, "layout", s.newBase(lang, "/")); terr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(buf.Bytes())
			return
		}
	}
	http.Error(w, locale.T(lang, "error.generic", nil), http.StatusInternalServerError)
}

func (s *server) notFound(w http.ResponseWriter, req *http.Request) {
	lang := s.visitorLang(w, req)
	s.render(w, http.StatusNotFound, "notfound", lang, s.newBase(lang, "/"))
}

type personData struct {
	base
	Token   string
	View    round.PersonView
	Error   string // already translated
	Draft   round.ItemInput
	EditID  int64 // item whose edit form failed, with its draft
	EditErr bool
}

func (s *server) personPage(w http.ResponseWriter, req *http.Request) {
	lang := s.visitorLang(w, req)
	s.showPerson(w, req, lang, http.StatusOK, nil, round.ItemInput{}, 0)
}

// showPerson renders the page; err (if any) is translated and shown.
func (s *server) showPerson(w http.ResponseWriter, req *http.Request, lang string, status int, formErr error, draft round.ItemInput, editID int64) {
	token := req.PathValue("token")
	v, err := s.r.PersonView(req.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, http.StatusNotFound, "notfound", lang, s.newBase(lang, "/"))
		return
	}
	if err != nil {
		s.fail(w, lang, err)
		return
	}
	d := personData{base: s.newBase(lang, "/p/"+token), Token: token, View: v, Draft: draft, EditID: editID}
	if formErr != nil {
		d.Error = locale.T(lang, errorKey(formErr), nil)
	}
	s.render(w, status, "person", lang, d)
}

func errorKey(err error) string {
	var ie *round.ItemError
	switch {
	case errors.As(err, &ie):
		return ie.Key
	case errors.Is(err, round.ErrNotEditable):
		return "person.frozen_error"
	case errors.Is(err, store.ErrNotFound):
		return "person.item_not_found"
	}
	return "person.error_generic"
}

func formInput(req *http.Request) round.ItemInput {
	return round.ItemInput{Title: req.PostFormValue("title"), Link: req.PostFormValue("link"), Note: req.PostFormValue("note")}
}

// afterPost redirects on success and re-renders with the error otherwise.
// An unknown person token is a 404 whatever the action.
func (s *server) afterPost(w http.ResponseWriter, req *http.Request, err error, draft round.ItemInput, editID int64) {
	if err == nil {
		http.Redirect(w, req, "/p/"+req.PathValue("token"), http.StatusSeeOther)
		return
	}
	lang := s.visitorLang(w, req)
	if _, perr := s.r.PersonView(req.Context(), req.PathValue("token")); perr != nil {
		s.showPerson(w, req, lang, http.StatusOK, err, draft, editID) // renders 404 or 500
		return
	}
	var ie *round.ItemError
	if !errors.As(err, &ie) && !errors.Is(err, round.ErrNotEditable) && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, lang, err)
		return
	}
	s.showPerson(w, req, lang, http.StatusOK, err, draft, editID)
}

func (s *server) personAdd(w http.ResponseWriter, req *http.Request) {
	in := formInput(req)
	s.afterPost(w, req, s.r.AddItem(req.Context(), req.PathValue("token"), in), in, 0)
}

func itemID(req *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *server) personUpdate(w http.ResponseWriter, req *http.Request) {
	in := formInput(req)
	id, ok := itemID(req)
	err := store.ErrNotFound
	if ok {
		err = s.r.UpdateItem(req.Context(), req.PathValue("token"), id, in)
	}
	s.afterPost(w, req, err, in, id)
}

func (s *server) personDelete(w http.ResponseWriter, req *http.Request) {
	id, ok := itemID(req)
	err := store.ErrNotFound
	if ok {
		err = s.r.DeleteItem(req.Context(), req.PathValue("token"), id)
	}
	s.afterPost(w, req, err, round.ItemInput{}, 0)
}
