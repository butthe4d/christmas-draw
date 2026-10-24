package web

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/store"
)

// flashKeys are the only messages ?msg= may select.
var flashKeys = map[string]bool{
	"admin.flash.greetings_sent": true,
	"admin.flash.reminders_sent": true,
	"admin.flash.greeting_sent":  true,
	"admin.flash.result_sent":    true,
	"admin.flash.results_sent":   true,
	"admin.flash.draw_done":      true,
	"admin.flash.draw_dry":       true,
	"admin.flash.change_sent":    true,
	"admin.flash.notice_sent":    true,
	"admin.flash.test_sent":      true,
	"admin.flash.wrong_state":    true,
	"admin.flash.error":          true,
}

// dryRunKeys are the flash wordings for batches that only went to the dry-run log.
var dryRunKeys = []string{
	"admin.flash.greetings_sent", "admin.flash.reminders_sent", "admin.flash.greeting_sent",
	"admin.flash.result_sent", "admin.flash.results_sent", "admin.flash.change_sent",
	"admin.flash.notice_sent",
}

func init() {
	for _, k := range dryRunKeys {
		flashKeys[k+"_dryrun"] = true
	}
}

type previewView struct {
	round.Preview
	ProblemText []string
	Currency    string
}

type flashView struct {
	Key    string
	N      string
	ID     string // provider ID of the SMS test
	To     string // the organiser's number, from the config
	Failed []string
}

type adminData struct {
	base
	Token        string
	Ov           round.Overview
	Feasibility  string // translated; "" when feasible
	DryLog       string
	Flash        *flashView
	Errors       []string
	NeedConfirm  bool
	LastTestAt   string
	DrawProblems []string
	Greetings    *previewView
	Reminders    *previewView
	Results      *previewView
	Change       *previewView
	ChangeText   string
	UnsealedDate string
	UnsealedTime string
}

// adminExtra carries what a POST wants shown on top of the normal page.
type adminExtra struct {
	errs         []string
	needConfirm  bool
	drawProblems []string
	change       *previewView
	changeText   string
}

type adminHandler func(w http.ResponseWriter, req *http.Request, lang, tok string)

// admin wraps a handler with the admin token check. A wrong token gets the
// same 404 as an unknown person token.
func (s *server) admin(h adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tok := req.PathValue("token")
		lang := s.visitorLang(w, req)
		want, err := s.r.AdminToken(req.Context())
		if err != nil {
			s.fail(w, lang, err)
			return
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
			s.render(w, http.StatusNotFound, "notfound", lang, s.newBase(lang, "/"))
			return
		}
		h(w, req, lang, tok)
	}
}

func (s *server) msgs(lang string, ms []locale.Msg) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.In(lang))
	}
	return out
}

func (s *server) view(lang string, p round.Preview) *previewView {
	return &previewView{Preview: p, ProblemText: s.msgs(lang, p.Problems), Currency: s.cfg.SMS.Currency}
}

func (s *server) adminPage(w http.ResponseWriter, req *http.Request, lang, tok string) {
	s.showAdmin(w, req, lang, tok, http.StatusOK, adminExtra{})
}

func (s *server) showAdmin(w http.ResponseWriter, req *http.Request, lang, tok string, status int, x adminExtra) {
	ctx := req.Context()
	if err := s.r.RefreshStatuses(ctx); err != nil {
		slog.Warn("status refresh failed", "err", err)
	}
	ov, err := s.r.Overview(ctx)
	if err != nil {
		s.fail(w, lang, err)
		return
	}
	d := adminData{
		base: s.newBase(lang, "/admin/"+tok), Token: tok, Ov: ov,
		Errors: x.errs, NeedConfirm: x.needConfirm,
		DrawProblems: x.drawProblems, Change: x.change, ChangeText: x.changeText,
	}
	if d.ChangeText == "" {
		d.ChangeText = s.cfg.EmergencyTemplate
	}
	if ov.Infeasible != nil {
		d.Feasibility = ov.Infeasible.In(lang)
	}
	if ov.DryRun {
		d.DryLog = s.r.DryRunLogPath()
	}
	if ov.UnsealedAt != nil {
		d.UnsealedDate = ov.UnsealedAt.Local().Format("2006-01-02")
		d.UnsealedTime = ov.UnsealedAt.Local().Format("15:04")
	}
	d.Flash = flashFrom(req, ov)
	if d.Flash != nil {
		d.Flash.To = s.cfg.OrganiserPhone
	}
	if ov.LastTest != nil {
		d.LastTestAt = ov.LastTest.SentAt.Local().Format("2006-01-02 15:04")
	}

	pv := func(p round.Preview, err error) *previewView {
		if err != nil {
			if !errors.Is(err, round.ErrWrongState) {
				slog.Error("preview failed", "err", err)
			}
			return nil
		}
		return s.view(lang, p)
	}
	switch ov.State {
	case round.Setup:
		d.Greetings = pv(s.r.PreviewGreetings(ctx))
	case round.Collecting:
		if ov.Ungreeted > 0 {
			d.Greetings = pv(s.r.PreviewGreetings(ctx))
		}
		d.Reminders = pv(s.r.PreviewReminders(ctx))
	case round.Drawn:
		d.Results = pv(s.r.PreviewResults(ctx))
	}
	s.render(w, status, "admin", lang, d)
}

// flashFrom reads ?msg=&n=&failed=. Only known message keys are shown, the
// count is a bounded number, and failed names must be people in the round.
func flashFrom(req *http.Request, ov round.Overview) *flashView {
	q := req.URL.Query()
	key := q.Get("msg")
	if !flashKeys[key] {
		return nil
	}
	n, err := strconv.Atoi(q.Get("n"))
	if err != nil || n < 0 || n > 100000 {
		n = 0
	}
	f := &flashView{Key: key, N: strconv.Itoa(n)}
	if id, err := strconv.ParseInt(q.Get("id"), 10, 64); err == nil && id > 0 {
		f.ID = strconv.FormatInt(id, 10)
	}
	known := map[string]bool{}
	for _, p := range ov.People {
		known[p.Name] = true
	}
	for _, name := range q["failed"] {
		if known[name] {
			f.Failed = append(f.Failed, name)
		}
	}
	return f
}

func (s *server) redirect(w http.ResponseWriter, req *http.Request, tok, key string, n int, failed []string) {
	u := "/admin/" + tok + "?msg=" + key + "&n=" + strconv.Itoa(n)
	for _, name := range failed {
		u += "&failed=" + url.QueryEscape(name)
	}
	http.Redirect(w, req, u, http.StatusSeeOther)
}

// actionError handles an action error: wrong state and unexpected errors become a
// flash, problems that carry a message are shown on the page.
func (s *server) actionError(w http.ResponseWriter, req *http.Request, lang, tok string, err error) {
	var m locale.Msg
	switch {
	case errors.Is(err, round.ErrWrongState):
		s.redirect(w, req, tok, "admin.flash.wrong_state", 0, nil)
	case errors.As(err, &m):
		s.showAdmin(w, req, lang, tok, http.StatusOK, adminExtra{errs: []string{m.In(lang)}})
	default:
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("admin action failed", "err", err)
		}
		s.redirect(w, req, tok, "admin.flash.error", 0, nil)
	}
}

// confirmed reports whether the confirm box was ticked; if not it re-renders
// the page asking for confirmation.
func (s *server) confirmed(w http.ResponseWriter, req *http.Request, lang, tok string, x adminExtra) bool {
	if req.PostFormValue("confirm") == "yes" {
		return true
	}
	x.needConfirm = true
	s.showAdmin(w, req, lang, tok, http.StatusOK, x)
	return false
}

func (s *server) outcome(w http.ResponseWriter, req *http.Request, lang, tok, key string, out round.Outcome, err error) {
	if err != nil {
		s.actionError(w, req, lang, tok, err)
		return
	}
	if out.DryRun {
		key += "_dryrun"
	}
	s.redirect(w, req, tok, key, out.Sent, out.Failed)
}

func (s *server) adminGreetings(w http.ResponseWriter, req *http.Request, lang, tok string) {
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.SendGreetings(req.Context())
	s.outcome(w, req, lang, tok, "admin.flash.greetings_sent", out, err)
}

func (s *server) adminReminders(w http.ResponseWriter, req *http.Request, lang, tok string) {
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.SendReminders(req.Context())
	s.outcome(w, req, lang, tok, "admin.flash.reminders_sent", out, err)
}

func personID(req *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *server) adminResendGreeting(w http.ResponseWriter, req *http.Request, lang, tok string) {
	id, ok := personID(req)
	if !ok {
		s.actionError(w, req, lang, tok, store.ErrNotFound)
		return
	}
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.ResendGreeting(req.Context(), id)
	s.outcome(w, req, lang, tok, "admin.flash.greeting_sent", out, err)
}

func (s *server) adminResendResult(w http.ResponseWriter, req *http.Request, lang, tok string) {
	id, ok := personID(req)
	if !ok {
		s.actionError(w, req, lang, tok, store.ErrNotFound)
		return
	}
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.ResendResult(req.Context(), id)
	s.outcome(w, req, lang, tok, "admin.flash.result_sent", out, err)
}

func (s *server) adminResendChangeNotice(w http.ResponseWriter, req *http.Request, lang, tok string) {
	id, ok := personID(req)
	if !ok {
		s.actionError(w, req, lang, tok, store.ErrNotFound)
		return
	}
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.ResendChangeNotice(req.Context(), id)
	s.outcome(w, req, lang, tok, "admin.flash.notice_sent", out, err)
}

func (s *server) adminTestSMS(w http.ResponseWriter, req *http.Request, lang, tok string) {
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	res, err := s.r.SendTestSMS(req.Context())
	if err != nil {
		s.actionError(w, req, lang, tok, err)
		return
	}
	// Post-redirect-get: a refresh must never send another paid SMS.
	http.Redirect(w, req, "/admin/"+tok+"?msg=admin.flash.test_sent&id="+strconv.FormatInt(res.ProviderID, 10), http.StatusSeeOther)
}

func (s *server) adminDraw(w http.ResponseWriter, req *http.Request, lang, tok string) {
	// Drawing again replaces the sealed draw, so it needs confirmation.
	st, err := s.r.State(req.Context())
	if err != nil {
		s.fail(w, lang, err)
		return
	}
	if st == round.Drawn && !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.Draw(req.Context())
	switch {
	case err != nil:
		s.actionError(w, req, lang, tok, err)
	case len(out.Problems) > 0:
		s.showAdmin(w, req, lang, tok, http.StatusOK, adminExtra{drawProblems: s.msgs(lang, out.Problems)})
	case out.DryRun:
		s.redirect(w, req, tok, "admin.flash.draw_dry", 0, nil)
	default:
		s.redirect(w, req, tok, "admin.flash.draw_done", 0, nil)
	}
}

func (s *server) adminResults(w http.ResponseWriter, req *http.Request, lang, tok string) {
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	out, err := s.r.SendResults(req.Context())
	s.outcome(w, req, lang, tok, "admin.flash.results_sent", out, err)
}

type unsealData struct {
	base
	Token string
	Pairs []round.Pair
}

// adminUnseal answers the POST directly: the pairs are never in a redirect,
// a GET URL or a cacheable response (render sets no-store).
func (s *server) adminUnseal(w http.ResponseWriter, req *http.Request, lang, tok string) {
	if !s.confirmed(w, req, lang, tok, adminExtra{}) {
		return
	}
	pairs, err := s.r.Unseal(req.Context())
	if err != nil {
		s.actionError(w, req, lang, tok, err)
		return
	}
	s.render(w, http.StatusOK, "unseal", lang, unsealData{base: s.newBase(lang, "/admin/"+tok), Token: tok, Pairs: pairs})
}

func (s *server) adminChange(w http.ResponseWriter, req *http.Request, lang, tok string) {
	text := req.PostFormValue("text")
	ctx := req.Context()
	if req.PostFormValue("action") == "send" {
		if !s.confirmed(w, req, lang, tok, adminExtra{changeText: text}) {
			return
		}
		out, err := s.r.ConfirmChange(ctx, text)
		s.outcome(w, req, lang, tok, "admin.flash.change_sent", out, err)
		return
	}
	p, err := s.r.PreviewEmergency(ctx, text)
	if err != nil {
		s.actionError(w, req, lang, tok, err)
		return
	}
	s.showAdmin(w, req, lang, tok, http.StatusOK, adminExtra{change: s.view(lang, p), changeText: text})
}
