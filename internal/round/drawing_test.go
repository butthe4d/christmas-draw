package round

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

var pZenek = testPerson{"Zenek", "+48600100500"}

// addItems gives every person one item titled "Gift for <name>".
func addItems(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	people, err := st.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		items, _ := st.Items(ctx, p.ID)
		if len(items) > 0 {
			continue
		}
		if _, err := st.AddItem(ctx, store.Item{PersonID: p.ID, Title: "Gift for " + p.Name}); err != nil {
			t.Fatal(err)
		}
	}
}

// drawnRound returns a synced round in Drawn with everyone's items present.
func drawnRound(t *testing.T, yaml string) (*Round, *store.Store, *sender.Fake, string) {
	t.Helper()
	r, st, fake, dry := newTestRound(t, yaml)
	mustSync(t, r)
	setState(t, st, Collecting)
	addItems(t, st)
	out, err := r.Draw(context.Background())
	if err != nil || len(out.Problems) != 0 {
		t.Fatalf("Draw: %+v %v", out, err)
	}
	return r, st, fake, dry
}

func deliveredRound(t *testing.T, yaml string) (*Round, *store.Store, *sender.Fake, string) {
	t.Helper()
	r, st, fake, dry := drawnRound(t, yaml)
	if _, err := r.SendResults(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Sent = nil
	return r, st, fake, dry
}

func mustState(t *testing.T, r *Round, want State) {
	t.Helper()
	got, err := r.State(context.Background())
	if err != nil || got != want {
		t.Fatalf("state = %q (%v), want %q", got, err, want)
	}
}

func hasProblem(ps []locale.Msg, want locale.Msg) bool {
	for _, p := range ps {
		if p.Key == want.Key && p.In("en") == want.In("en") {
			return true
		}
	}
	return false
}

func TestDrawBlockedEmptyList(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	setState(t, st, Collecting)
	for _, n := range []string{"Maciej", "Ania"} {
		st.AddItem(ctx, store.Item{PersonID: personByName(t, st, n).ID, Title: "x"})
	}
	out, err := r.Draw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(out.Problems, locale.M("draw.empty_list", "name", "Tomek")) {
		t.Fatalf("problems = %+v", out.Problems)
	}
	mustState(t, r, Collecting)
	if pairs, _ := st.Pairs(ctx); len(pairs) != 0 {
		t.Fatalf("pairs saved: %v", pairs)
	}
}

func TestDrawInfeasible(t *testing.T) {
	ctx := context.Background()
	y := testYAML("couples:\n  - [Maciej, Ania]\nexclude_couples: true\n", pMaciej, pAnia, pTomek)
	r, st, _, _ := newTestRound(t, y)
	mustSync(t, r)
	setState(t, st, Collecting)
	addItems(t, st)
	out, err := r.Draw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(out.Problems, locale.M("draw.infeasible", "givers", "Ania, Maciej", "receivers", "Tomek")) {
		t.Fatalf("problems = %+v", out.Problems)
	}
	mustState(t, r, Collecting)
}

func TestDrawResultSMSTooLong(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("a", 600)
	y := testYAML("result_template: \"{receiver} "+long+" {link}\"\n", pMaciej, pAnia, pTomek)
	r, st, _, _ := newTestRound(t, y)
	mustSync(t, r)
	setState(t, st, Collecting)
	addItems(t, st)
	out, err := r.Draw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Problems) == 0 || out.Problems[0].Key != "problem.sms_too_long" {
		t.Fatalf("problems = %+v", out.Problems)
	}
	mustState(t, r, Collecting)
	if pairs, _ := st.Pairs(ctx); len(pairs) != 0 {
		t.Fatal("pairs saved")
	}
}

func TestDrawSaves(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := drawnRound(t, defaultYAML)
	mustState(t, r, Drawn)
	pairs, err := r.Unseal(ctx)
	if err != nil || len(pairs) != 3 {
		t.Fatalf("%v %v", pairs, err)
	}
	a := draw.Assignment{}
	for _, p := range pairs {
		a[p.Giver] = p.Receiver
	}
	if err := draw.Check(r.Rules(), a); err != nil {
		t.Fatal(err)
	}
	if saved, _ := st.Pairs(ctx); len(saved) != 3 {
		t.Fatalf("saved %v", saved)
	}
}

func TestDrawDryRun(t *testing.T) {
	ctx := context.Background()
	y := strings.Replace(defaultYAML, "dry_run: false", "dry_run: true", 1)
	r, st, fake, dry := newTestRound(t, y)
	mustSync(t, r)
	setState(t, st, Collecting)
	addItems(t, st)
	out, err := r.Draw(ctx)
	if err != nil || !out.DryRun || len(out.Problems) != 0 || out.LogPath != dry {
		t.Fatalf("%+v %v", out, err)
	}
	if pairs, _ := st.Pairs(ctx); len(pairs) != 0 {
		t.Fatal("pairs saved in dry-run")
	}
	mustState(t, r, Collecting)
	if len(fake.Sent) != 0 {
		t.Fatal("sent in dry-run")
	}
	b, err := os.ReadFile(dry)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	masked := message.Link("https://x.test", message.MaskedToken)
	if n := strings.Count(log, masked); n != 3 {
		t.Fatalf("masked links = %d\n%s", n, log)
	}
	for _, p := range []string{"Maciej", "Ania", "Tomek"} {
		if tok := personByName(t, st, p).Token; strings.Contains(log, tok) {
			t.Fatalf("log leaks token of %s", p)
		}
	}
	if ms, _ := st.Messages(ctx); len(ms) != 0 {
		t.Fatal("messages recorded in dry-run")
	}
}

func TestRedraw(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := drawnRound(t, defaultYAML)
	out, err := r.Draw(ctx)
	if err != nil || len(out.Problems) != 0 {
		t.Fatalf("redraw: %+v %v", out, err)
	}
	mustState(t, r, Drawn)
	setState(t, st, Delivered)
	if _, err := r.Draw(ctx); !errors.Is(err, ErrWrongState) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendResults(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := drawnRound(t, defaultYAML)
	pv, err := r.PreviewResults(ctx)
	if err != nil || pv.Sample != "" || pv.Messages != 3 || pv.Segments < 3 || len(pv.Problems) != 0 {
		t.Fatalf("preview %+v %v", pv, err)
	}
	out, err := r.SendResults(ctx)
	if err != nil || out.Sent != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	mustState(t, r, Delivered)
	pairs, _ := r.Unseal(ctx)
	recv := map[string]string{}
	for _, p := range pairs {
		recv[p.Giver] = p.Receiver
	}
	for _, p := range []testPerson{pMaciej, pAnia, pTomek} {
		var found bool
		for _, m := range fake.Sent {
			if m.To != p.Phone {
				continue
			}
			found = true
			if !strings.Contains(m.Text, recv[p.Name]) || !strings.Contains(m.Text, "Gift for "+recv[p.Name]) ||
				!strings.Contains(m.Text, message.Link("https://x.test", personByName(t, st, p.Name).Token)) {
				t.Errorf("bad SMS for %s: %q", p.Name, m.Text)
			}
		}
		if !found {
			t.Errorf("no SMS to %s", p.Name)
		}
	}
	if countKind(t, st, "result") != 3 {
		t.Fatal("results not recorded")
	}
	if _, err := r.SendResults(ctx); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second send: %v", err)
	}
}

func TestSendResultsPartialFailureStillDelivered(t *testing.T) {
	ctx := context.Background()
	r, _, fake, _ := drawnRound(t, defaultYAML)
	fake.FailTo = map[string]bool{pTomek.Phone: true}
	out, err := r.SendResults(ctx)
	if err != nil || out.Sent != 2 || len(out.Failed) != 1 || out.Failed[0] != "Tomek" {
		t.Fatalf("%+v %v", out, err)
	}
	mustState(t, r, Delivered)
	fake.FailTo = nil
	fake.Sent = nil
	tomek, _ := r.st.People(ctx)
	var id int64
	for _, p := range tomek {
		if p.Name == "Tomek" {
			id = p.ID
		}
	}
	out, err = r.ResendResult(ctx, id)
	if err != nil || out.Sent != 1 || len(fake.Sent) != 1 || fake.Sent[0].To != pTomek.Phone {
		t.Fatalf("%+v %v %v", out, err, fake.Sent)
	}
}

func TestConcurrentSendResults(t *testing.T) {
	ctx := context.Background()
	r, _, fake, _ := drawnRound(t, defaultYAML)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.SendResults(ctx)
		}()
	}
	wg.Wait()
	if len(fake.Sent) != 3 {
		t.Fatalf("sent %d", len(fake.Sent))
	}
	wrong := 0
	for _, e := range errs {
		if errors.Is(e, ErrWrongState) {
			wrong++
		} else if e != nil {
			t.Fatal(e)
		}
	}
	if wrong != 1 {
		t.Fatalf("errs = %v", errs)
	}
}

func TestResendResultWrongState(t *testing.T) {
	r, st, _, _ := drawnRound(t, defaultYAML)
	if _, err := r.ResendResult(context.Background(), personByName(t, st, "Ania").ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnseal(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := drawnRound(t, defaultYAML)
	pairs, err := r.Unseal(ctx)
	if err != nil || len(pairs) != 3 {
		t.Fatalf("%v %v", pairs, err)
	}
	for i := 1; i < len(pairs); i++ {
		if pairs[i-1].Giver > pairs[i].Giver {
			t.Fatalf("not sorted: %v", pairs)
		}
	}
	saved, _ := st.Pairs(ctx)
	people, _ := st.People(ctx)
	name := map[int64]string{}
	for _, p := range people {
		name[p.ID] = p.Name
	}
	for g, rc := range saved {
		ok := false
		for _, p := range pairs {
			ok = ok || (p.Giver == name[g] && p.Receiver == name[rc])
		}
		if !ok {
			t.Fatalf("pair %s->%s missing in %v", name[g], name[rc], pairs)
		}
	}
	ov, _ := r.Overview(ctx)
	if ov.UnsealedAt == nil || !ov.UnsealedAt.Equal(fixedNow) {
		t.Fatalf("UnsealedAt = %v", ov.UnsealedAt)
	}
	later := fixedNow.Add(time.Hour)
	r.opts.Now = func() time.Time { return later }
	if _, err := r.Unseal(ctx); err != nil {
		t.Fatal(err)
	}
	ov, _ = r.Overview(ctx)
	if !ov.UnsealedAt.Equal(fixedNow) {
		t.Fatalf("UnsealedAt moved to %v", ov.UnsealedAt)
	}
	setState(t, st, Setup)
	if _, err := r.Unseal(ctx); !errors.Is(err, ErrWrongState) {
		t.Fatalf("err = %v", err)
	}
}

func fourYAML() string { return testYAML("", pMaciej, pAnia, pTomek, pOla) }

func TestConfirmChangeRemove(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := deliveredRound(t, fourYAML())
	tomekTok := personByName(t, st, "Tomek").Token
	reconfigure(t, r, testYAML("", pMaciej, pAnia, pOla))
	mustSync(t, r)
	mustState(t, r, ChangePending)

	pv, err := r.PreviewEmergency(ctx, "Zmiana! {link}")
	if err != nil || pv.Messages != 3 || len(pv.Problems) != 0 {
		t.Fatalf("%+v %v", pv, err)
	}
	out, err := r.ConfirmChange(ctx, "Zmiana! {link}")
	if err != nil || out.Sent != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	if len(fake.Sent) != 3 {
		t.Fatalf("sent %d", len(fake.Sent))
	}
	for _, m := range fake.Sent {
		if m.To == pTomek.Phone {
			t.Fatal("removed person texted")
		}
		if !strings.HasPrefix(m.Text, "Zmiana! https://x.test/p/") || strings.Contains(m.Text, "XXXX") {
			t.Errorf("text %q", m.Text)
		}
	}
	if _, err := st.PersonByToken(ctx, tomekTok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if pairs, _ := st.Pairs(ctx); len(pairs) != 0 {
		t.Fatal("pairs remain")
	}
	mustState(t, r, Collecting)
	people, _ := st.People(ctx)
	counts, _ := st.ItemCounts(ctx)
	if len(people) != 3 {
		t.Fatalf("people %d", len(people))
	}
	for _, p := range people {
		if counts[p.ID] != 1 {
			t.Errorf("%s lost items", p.Name)
		}
	}
	if countKind(t, st, "emergency") != 3 {
		t.Fatal("emergency not recorded")
	}
	// rules_hash is current: a new Sync sees no change.
	if rep := mustSync(t, r); rep.Diff.Structural() || rep.NewState != Collecting {
		t.Fatalf("rep %+v", rep)
	}
}

func TestConfirmChangeAdd(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := deliveredRound(t, defaultYAML)
	reconfigure(t, r, testYAML("", pMaciej, pAnia, pTomek, pZenek))
	mustSync(t, r)
	mustState(t, r, ChangePending)
	pv, err := r.PreviewEmergency(ctx, "Zmiana!")
	if err != nil || pv.Messages != 4 {
		t.Fatalf("%+v %v", pv, err)
	}
	out, err := r.ConfirmChange(ctx, "Zmiana!")
	if err != nil || out.Sent != 4 {
		t.Fatalf("%+v %v", out, err)
	}
	var emerg, greet int
	zenek := personByName(t, st, "Zenek")
	for _, m := range fake.Sent {
		switch {
		case m.Text == "Zmiana!":
			emerg++
			if m.To == pZenek.Phone {
				t.Fatal("newcomer got emergency")
			}
		case m.To == pZenek.Phone && strings.Contains(m.Text, message.Link("https://x.test", zenek.Token)):
			greet++
		}
	}
	if emerg != 3 || greet != 1 {
		t.Fatalf("emerg=%d greet=%d sent=%v", emerg, greet, fake.Sent)
	}
	if !zenek.Greeted {
		t.Fatal("newcomer not marked greeted")
	}
	mustState(t, r, Collecting)
	d, err := r.Draw(ctx)
	if err != nil || !hasProblem(d.Problems, locale.M("draw.empty_list", "name", "Zenek")) {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestConfirmChangeDryRun(t *testing.T) {
	ctx := context.Background()
	r, st, fake, dry := deliveredRound(t, fourYAML())
	dryYAML := strings.Replace(testYAML("", pMaciej, pAnia, pOla, pZenek), "dry_run: false", "dry_run: true", 1)
	reconfigure(t, r, dryYAML)
	mustSync(t, r)
	mustState(t, r, ChangePending)
	beforePeople, _ := st.People(ctx)
	beforePairs, _ := st.Pairs(ctx)
	beforeMsgs, _ := st.Messages(ctx)

	out, err := r.ConfirmChange(ctx, "Zmiana! {link}")
	if err != nil || !out.DryRun || out.LogPath != dry {
		t.Fatalf("%+v %v", out, err)
	}
	mustState(t, r, ChangePending)
	afterPeople, _ := st.People(ctx)
	afterPairs, _ := st.Pairs(ctx)
	afterMsgs, _ := st.Messages(ctx)
	if len(afterPeople) != len(beforePeople) || len(afterPairs) != len(beforePairs) || len(afterMsgs) != len(beforeMsgs) {
		t.Fatal("dry-run changed the database")
	}
	if len(fake.Sent) != 0 {
		t.Fatal("sent in dry-run")
	}
	b, _ := os.ReadFile(dry)
	log := string(b)
	masked := message.Link("https://x.test", message.MaskedToken)
	if !strings.Contains(log, "Zmiana! "+masked) || !strings.Contains(log, "Hi Zenek") {
		t.Fatalf("log:\n%s", log)
	}
	if strings.Count(log, "Zmiana! "+masked) != 3 {
		t.Fatalf("log:\n%s", log)
	}
	for _, p := range beforePeople {
		if strings.Contains(log, p.Token) {
			t.Fatal("log leaks token")
		}
	}
}

func TestConfirmChangeTooLong(t *testing.T) {
	ctx := context.Background()
	r, st, fake, _ := deliveredRound(t, fourYAML())
	reconfigure(t, r, testYAML("", pMaciej, pAnia, pOla))
	mustSync(t, r)
	if _, err := r.ConfirmChange(ctx, strings.Repeat("a", 400)); err == nil {
		t.Fatal("expected error")
	}
	mustState(t, r, ChangePending)
	if len(fake.Sent) != 0 {
		t.Fatal("sent")
	}
	if people, _ := st.People(ctx); len(people) != 4 {
		t.Fatal("people changed")
	}
	if pairs, _ := st.Pairs(ctx); len(pairs) != 4 {
		t.Fatal("pairs cleared")
	}
}

func TestConfirmChangeWrongState(t *testing.T) {
	r, _, _, _ := deliveredRound(t, defaultYAML)
	if _, err := r.ConfirmChange(context.Background(), "x"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("err = %v", err)
	}
}
