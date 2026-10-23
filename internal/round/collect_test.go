package round

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/store"
)

func countKind(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	ms, err := st.Messages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range ms {
		if m.Kind == kind {
			n++
		}
	}
	return n
}

func TestSendGreetingsAdvances(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	out, err := r.SendGreetings(ctx)
	if err != nil || out.Sent != 3 || len(out.Failed) != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(fake.Sent) != 3 {
		t.Fatalf("sent %d", len(fake.Sent))
	}
	for _, p := range []testPerson{pMaciej, pAnia, pTomek} {
		tok := personByName(t, st, p.Name).Token
		found := false
		for _, m := range fake.Sent {
			if m.To == p.Phone {
				found = true
				if !strings.Contains(m.Text, message.Link("https://x.test", tok)) || strings.Contains(m.Text, "XXXX") {
					t.Errorf("bad text for %s: %q", p.Name, m.Text)
				}
			}
		}
		if !found {
			t.Errorf("no SMS to %s", p.Name)
		}
		if !personByName(t, st, p.Name).Greeted {
			t.Errorf("%s not greeted", p.Name)
		}
	}
	if s, _ := r.State(ctx); s != Collecting {
		t.Errorf("state %s", s)
	}
	if n := countKind(t, st, "greeting"); n != 3 {
		t.Errorf("messages %d", n)
	}
}

func TestSendGreetingsDryRun(t *testing.T) {
	ctx := context.Background()
	r, st, fake, dry := newTestRound(t, strings.Replace(defaultYAML, "dry_run: false", "dry_run: true", 1))
	mustSync(t, r)
	out, err := r.SendGreetings(ctx)
	if err != nil || !out.DryRun || out.LogPath != dry {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(fake.Sent) != 0 {
		t.Fatal("sent in dry run")
	}
	b, _ := os.ReadFile(dry)
	log := string(b)
	if strings.Count(log, "/p/XXXXXXXXXXXXXXXX") != 3 {
		t.Errorf("log: %s", log)
	}
	for _, p := range []string{"Maciej", "Ania", "Tomek"} {
		if strings.Contains(log, personByName(t, st, p).Token) {
			t.Error("real token in log")
		}
	}
	if s, _ := r.State(ctx); s != Setup {
		t.Errorf("state %s", s)
	}
	if ms, _ := st.Messages(ctx); len(ms) != 0 {
		t.Error("messages recorded")
	}
}

func TestGreetingsOnlyUngreeted(t *testing.T) {
	ctx := context.Background()
	r, _, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	if _, err := r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	fake.Sent = nil
	reconfigure(t, r, testYAML("", pMaciej, pAnia, pTomek, testPerson{"Zenek", "+48600100500"}))
	mustSync(t, r)
	if _, err := r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.Sent) != 1 || fake.Sent[0].To != "+48600100500" {
		t.Fatalf("sent %+v", fake.Sent)
	}
}

func TestPartialFailure(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	fake.FailTo = map[string]bool{pTomek.Phone: true}
	out, err := r.SendGreetings(ctx)
	if err != nil || out.Sent != 2 || len(out.Failed) != 1 || out.Failed[0] != "Tomek" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if s, _ := r.State(ctx); s != Collecting {
		t.Errorf("state %s", s)
	}
	if personByName(t, st, "Tomek").Greeted {
		t.Error("Tomek greeted")
	}
	fake.FailTo = nil
	fake.Sent = nil
	if _, err := r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.Sent) != 1 || fake.Sent[0].To != pTomek.Phone {
		t.Fatalf("sent %+v", fake.Sent)
	}
}

func TestAllFail(t *testing.T) {
	ctx := context.Background()
	r, _, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	fake.FailTo = map[string]bool{pMaciej.Phone: true, pAnia.Phone: true, pTomek.Phone: true}
	if _, err := r.SendGreetings(ctx); err == nil {
		t.Fatal("expected error")
	}
	if s, _ := r.State(ctx); s != Setup {
		t.Errorf("state %s", s)
	}
}

func TestReminders(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	if _, err := r.SendReminders(ctx); !errors.Is(err, ErrWrongState) {
		t.Fatalf("setup: %v", err)
	}
	if _, err := r.PreviewReminders(ctx); !errors.Is(err, ErrWrongState) {
		t.Fatalf("setup preview: %v", err)
	}
	setState(t, st, Collecting)
	if _, err := st.AddItem(ctx, store.Item{PersonID: personByName(t, st, "Ania").ID, Title: "Book"}); err != nil {
		t.Fatal(err)
	}
	out, err := r.SendReminders(ctx)
	if err != nil || out.Sent != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, m := range fake.Sent {
		if m.To == pAnia.Phone {
			t.Error("reminder to Ania")
		}
	}
	if n := countKind(t, st, "reminder"); n != 2 {
		t.Errorf("recorded %d", n)
	}
}

func TestPreviewGreetings(t *testing.T) {
	ctx := context.Background()
	r, _, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	p, err := r.PreviewGreetings(ctx)
	if err != nil || p.Messages != 3 || !strings.Contains(p.Sample, "XXXXXXXXXXXXXXXX") || len(p.Problems) != 0 {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	want := 0.14 * float64(p.Segments)
	if d := p.Cost - want; d > 1e-9 || d < -1e-9 || p.CostEstimated {
		t.Errorf("cost %v want %v est=%v", p.Cost, want, p.CostEstimated)
	}
	fake.PriceErr = errors.New("boom")
	p, _ = r.PreviewGreetings(ctx)
	if d := p.Cost - want; d > 1e-9 || d < -1e-9 || !p.CostEstimated {
		t.Errorf("cost %v est=%v", p.Cost, p.CostEstimated)
	}
	fake.PriceErr = nil

	r.cfg.Greeting = strings.Repeat("a", 400) + " {link}"
	p, err = r.PreviewGreetings(ctx)
	if err != nil || len(p.Problems) == 0 || p.Problems[0].Key != "problem.sms_too_long" {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if _, err := r.SendGreetings(ctx); err == nil {
		t.Fatal("expected error")
	}
	if len(fake.Sent) != 0 {
		t.Fatal("sent despite problems")
	}
}

func TestSendTestSMS(t *testing.T) {
	ctx := context.Background()
	for lang, word := range map[string]string{"": "code", "language: pl\n": "kod"} {
		y := strings.Replace(defaultYAML, "dry_run: false", "dry_run: true", 1)
		y = lang + "organiser_phone: \"+48600999999\"\n" + y
		r, st, fake, _ := newTestRound(t, y)
		mustSync(t, r)
		res, err := r.SendTestSMS(ctx)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`^christmas-draw test \d\d:\d\d ` + word + ` \d{4}$`)
		if len(fake.Sent) != 1 || fake.Sent[0].To != "+48600999999" || !re.MatchString(fake.Sent[0].Text) || res.Text != fake.Sent[0].Text {
			t.Errorf("sent %+v res %+v", fake.Sent, res)
		}
		if countKind(t, st, "test") != 1 {
			t.Error("test not recorded")
		}
	}
	r, _, _, _ := newTestRound(t, defaultYAML)
	if _, err := r.SendTestSMS(ctx); err == nil {
		t.Fatal("expected error without organiser_phone")
	}
}

func TestRefreshStatuses(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	if _, err := r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	fake.Statuses = map[int64]string{1: "DELIVERED"}
	if err := r.RefreshStatuses(ctx); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages(ctx)
	for _, m := range ms {
		if m.ProviderID == 1 && m.Status != "DELIVERED" {
			t.Errorf("status %q", m.Status)
		}
	}
	if err := r.RefreshStatuses(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.StatusCalls != 1 {
		t.Errorf("status calls %d", fake.StatusCalls)
	}
	// after the window it calls again
	now := fixedNow.Add(time.Minute)
	r.opts.Now = func() time.Time { return now }
	if err := r.RefreshStatuses(ctx); err != nil || fake.StatusCalls != 2 {
		t.Errorf("calls %d err %v", fake.StatusCalls, err)
	}
}

func TestResendGreeting(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	id := personByName(t, st, "Ania").ID
	if _, err := r.ResendGreeting(ctx, id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("setup: %v", err)
	}
	setState(t, st, Collecting)
	out, err := r.ResendGreeting(ctx, id)
	if err != nil || out.Sent != 1 || len(fake.Sent) != 1 || fake.Sent[0].To != pAnia.Phone {
		t.Fatalf("out=%+v err=%v sent=%+v", out, err, fake.Sent)
	}
}

func TestOverview(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	if _, err := r.SendGreetings(ctx); err != nil {
		t.Fatal(err)
	}
	ov, err := r.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.State != Collecting || len(ov.People) != 3 || ov.Ungreeted != 0 || ov.Infeasible != nil || ov.Pending != nil {
		t.Fatalf("%+v", ov)
	}
	for _, p := range ov.People {
		if p.LastKind != "greeting" || p.LastStatus != "QUEUED" || !p.Greeted {
			t.Errorf("%+v", p)
		}
	}
	_ = st
}

func TestInfeasibleMsg(t *testing.T) {
	y := testYAML("couples: []\nexclusions:\n  - [Maciej, Ania]\n  - [Maciej, Tomek]\n", pMaciej, pAnia, pTomek)
	r, _, _, _ := newTestRound(t, y)
	mustSync(t, r)
	ov, err := r.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ov.Infeasible == nil || ov.Infeasible.Key != "draw.nobody_allowed" || ov.Infeasible.Args["name"] != "Maciej" {
		t.Fatalf("%+v", ov.Infeasible)
	}
}
