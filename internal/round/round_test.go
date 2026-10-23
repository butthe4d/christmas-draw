package round

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/store"
)

var ctx = context.Background()

func personByName(t *testing.T, st *store.Store, name string) store.Person {
	t.Helper()
	ps, err := st.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("person %q not found", name)
	return store.Person{}
}

func TestSyncFresh(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	rep := mustSync(t, r)
	if !rep.AdminTokenCreated || rep.NewState != Setup {
		t.Fatalf("report = %+v", rep)
	}
	ps, _ := st.People(ctx)
	if len(ps) != 3 {
		t.Fatalf("people = %d", len(ps))
	}
	re := regexp.MustCompile(`^[A-Z2-7]{16}$`)
	admin, err := r.AdminToken(ctx)
	if err != nil || !re.MatchString(admin) {
		t.Fatalf("admin token %q, %v", admin, err)
	}
	seen := map[string]bool{admin: true}
	for _, p := range ps {
		if !re.MatchString(p.Token) || seen[p.Token] {
			t.Errorf("bad or duplicate token %q", p.Token)
		}
		seen[p.Token] = true
		if p.Greeted {
			t.Errorf("%s greeted", p.Name)
		}
	}
	if s, _ := r.State(ctx); s != Setup {
		t.Errorf("state = %s", s)
	}
	rep2 := mustSync(t, r)
	if rep2.AdminTokenCreated {
		t.Error("admin token recreated")
	}
	if admin2, _ := r.AdminToken(ctx); admin2 != admin {
		t.Error("admin token changed")
	}
}

func TestSyncCosmeticEdits(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	ania := personByName(t, st, "Ania")
	if _, err := st.AddItem(ctx, store.Item{PersonID: ania.ID, Title: "Book"}); err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(defaultYAML, `name: "Ania"`, `name: "Ania "`, 1)
	y = strings.Replace(y, "+48600100200", "600 100 200", 1)
	reconfigure(t, r, y)
	rep := mustSync(t, r)
	d := rep.Diff
	if len(d.Added)+len(d.Removed)+len(d.PhoneChanged) != 0 || d.RulesChanged {
		t.Fatalf("diff not empty: %+v", d)
	}
	after := personByName(t, st, "Ania")
	if after.ID != ania.ID || after.Token != ania.Token {
		t.Error("person identity changed")
	}
	if items, _ := st.Items(ctx, ania.ID); len(items) != 1 {
		t.Errorf("items = %d", len(items))
	}
}

func fourPeople() string { return testYAML("", pMaciej, pAnia, pTomek, pOla) }

func TestSyncAddRemoveCollecting(t *testing.T) {
	r, st, _, _ := newTestRound(t, fourPeople())
	mustSync(t, r)
	setState(t, st, Collecting)
	ania := personByName(t, st, "Ania")
	tomek := personByName(t, st, "Tomek")
	st.AddItem(ctx, store.Item{PersonID: ania.ID, Title: "Book"})
	st.SetGreeted(ctx, ania.ID, true)

	reconfigure(t, r, testYAML("", pMaciej, pAnia, pOla, testPerson{"Zenek", "+48600100500"}))
	rep := mustSync(t, r)
	if rep.NewState != Collecting || len(rep.Diff.Added) != 1 || len(rep.Diff.Removed) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	z := personByName(t, st, "Zenek")
	if len(z.Token) != 16 || z.Greeted {
		t.Errorf("zenek = %+v", z)
	}
	if _, err := st.PersonByToken(ctx, tomek.Token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tomek token err = %v", err)
	}
	if items, _ := st.Items(ctx, ania.ID); len(items) != 1 {
		t.Errorf("ania items = %d", len(items))
	}
	if !personByName(t, st, "Ania").Greeted {
		t.Error("ania greeted flag lost")
	}
}

func TestSyncDrawnStructural(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	ps, _ := st.People(ctx)
	st.SavePairs(ctx, map[int64]int64{ps[0].ID: ps[1].ID, ps[1].ID: ps[2].ID, ps[2].ID: ps[0].ID})
	setState(t, st, Drawn)

	reconfigure(t, r, fourPeople())
	rep := mustSync(t, r)
	if rep.NewState != Collecting {
		t.Fatalf("state = %s", rep.NewState)
	}
	if pairs, _ := st.Pairs(ctx); len(pairs) != 0 {
		t.Errorf("pairs = %v", pairs)
	}
	personByName(t, st, "Ola")
}

func TestSyncDrawnNoChangeKeepsPairs(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	ps, _ := st.People(ctx)
	st.SavePairs(ctx, map[int64]int64{ps[0].ID: ps[1].ID, ps[1].ID: ps[2].ID, ps[2].ID: ps[0].ID})
	setState(t, st, Drawn)
	if rep := mustSync(t, r); rep.NewState != Drawn {
		t.Fatalf("state = %s", rep.NewState)
	}
	if pairs, _ := st.Pairs(ctx); len(pairs) != 3 {
		t.Errorf("pairs = %v", pairs)
	}
}

func TestSyncDeliveredPending(t *testing.T) {
	r, st, _, _ := newTestRound(t, fourPeople())
	mustSync(t, r)
	ps, _ := st.People(ctx)
	st.SavePairs(ctx, map[int64]int64{ps[0].ID: ps[1].ID, ps[1].ID: ps[2].ID, ps[2].ID: ps[3].ID, ps[3].ID: ps[0].ID})
	setState(t, st, Delivered)

	reconfigure(t, r, testYAML("", pMaciej, pAnia, pOla))
	rep := mustSync(t, r)
	if rep.NewState != ChangePending {
		t.Fatalf("state = %s", rep.NewState)
	}
	personByName(t, st, "Tomek")
	if pairs, _ := st.Pairs(ctx); len(pairs) != 4 {
		t.Errorf("pairs = %v", pairs)
	}
	// Still pending on a second sync.
	if rep := mustSync(t, r); rep.NewState != ChangePending {
		t.Fatalf("state = %s", rep.NewState)
	}
	reconfigure(t, r, fourPeople())
	if rep := mustSync(t, r); rep.NewState != Delivered {
		t.Fatalf("state after revert = %s", rep.NewState)
	}
}

func TestSyncPhoneOnlyDelivered(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	setState(t, st, Delivered)
	reconfigure(t, r, strings.Replace(defaultYAML, "+48600100300", "+48600999999", 1))
	rep := mustSync(t, r)
	if rep.NewState != Delivered || len(rep.Diff.PhoneChanged) != 1 || rep.Diff.PhoneChanged[0] != "Tomek" {
		t.Fatalf("report = %+v", rep)
	}
	if p := personByName(t, st, "Tomek"); p.Phone != "+48600999999" {
		t.Errorf("phone = %s", p.Phone)
	}
}

func TestSyncRulesChangeDelivered(t *testing.T) {
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	setState(t, st, Delivered)
	reconfigure(t, r, testYAML("exclusions: [[Ania, Tomek]]\n", pMaciej, pAnia, pTomek))
	rep := mustSync(t, r)
	if rep.NewState != ChangePending || !rep.Diff.RulesChanged {
		t.Fatalf("report = %+v", rep)
	}
}

func TestRulesHashIgnoresOrder(t *testing.T) {
	a := "couples: [[Ania, Tomek], [Maciej, Ola]]\nexclusions: [[Ania, Maciej], [Ola, Tomek]]\n"
	b := "couples: [[Ola, Maciej], [Tomek, Ania]]\nexclusions: [[Tomek, Ola], [Maciej, Ania]]\n"
	r, st, _, _ := newTestRound(t, testYAML(a, pMaciej, pAnia, pTomek, pOla))
	mustSync(t, r)
	setState(t, st, Delivered)
	reconfigure(t, r, testYAML(b, pMaciej, pAnia, pTomek, pOla))
	rep := mustSync(t, r)
	if rep.Diff.RulesChanged || rep.NewState != Delivered {
		t.Fatalf("report = %+v", rep)
	}
}

type constReader struct{ b byte }

func (c constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.b
	}
	return len(p), nil
}

func TestUniqueTokenRetriesOnCollision(t *testing.T) {
	r, _, _, _ := newTestRound(t, defaultYAML)
	first, _ := newToken(constReader{1})
	r.opts.Rand = &seqReader{chunks: [][]byte{
		{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, {2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
	}}
	tok, err := r.uniqueToken(map[string]bool{first: true})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := newToken(constReader{2}); tok != want {
		t.Errorf("tok = %s, want %s", tok, want)
	}
}

type seqReader struct {
	chunks [][]byte
}

func (s *seqReader) Read(p []byte) (int, error) {
	c := s.chunks[0]
	if len(s.chunks) > 1 {
		s.chunks = s.chunks[1:]
	}
	return copy(p, c), nil
}
