package round

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/store"
)

func tokenOf(t *testing.T, st *store.Store, name string) string {
	t.Helper()
	people, err := st.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		if p.Name == name {
			return p.Token
		}
	}
	t.Fatalf("no person %s", name)
	return ""
}

func TestEditableByState(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	tok := tokenOf(t, st, "Maciej")
	in := ItemInput{Title: "Book"}
	for _, s := range []State{Setup, Collecting} {
		setState(t, st, s)
		if err := r.AddItem(ctx, tok, in); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		v, err := r.PersonView(ctx, tok)
		if err != nil || !v.Editable {
			t.Fatalf("%s: editable=%v err=%v", s, v.Editable, err)
		}
	}
	for _, s := range []State{Drawn, Delivered} {
		setState(t, st, s)
		if err := r.AddItem(ctx, tok, in); !errors.Is(err, ErrNotEditable) {
			t.Fatalf("%s: got %v", s, err)
		}
		if v, _ := r.PersonView(ctx, tok); v.Editable {
			t.Fatalf("%s: should not be editable", s)
		}
	}

	r2, st2, _, _ := newTestRound(t, testYAML("lists_editable_after_draw: true\n", pMaciej, pAnia, pTomek))
	mustSync(t, r2)
	setState(t, st2, Delivered)
	if err := r2.AddItem(ctx, tokenOf(t, st2, "Maciej"), in); err != nil {
		t.Fatalf("after draw: %v", err)
	}
}

func TestReceiverOnlyAfterDelivery(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	people, _ := st.People(ctx)
	ids := map[string]int64{}
	for _, p := range people {
		ids[p.Name] = p.ID
	}
	if _, err := st.AddItem(ctx, store.Item{PersonID: ids["Ania"], Title: "Scarf", Link: "https://x.test/s", Note: "red"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePairs(ctx, map[int64]int64{ids["Maciej"]: ids["Ania"], ids["Ania"]: ids["Tomek"], ids["Tomek"]: ids["Maciej"]}); err != nil {
		t.Fatal(err)
	}
	tok := tokenOf(t, st, "Maciej")
	setState(t, st, Drawn)
	if v, _ := r.PersonView(ctx, tok); v.Receiver != nil {
		t.Fatal("receiver visible in Drawn")
	}
	for _, s := range []State{Delivered, ChangePending} {
		setState(t, st, s)
		v, err := r.PersonView(ctx, tok)
		if err != nil || v.Receiver == nil {
			t.Fatalf("%s: %v %v", s, v, err)
		}
		if v.Receiver.Name != "Ania" || len(v.Receiver.Items) != 1 || v.Receiver.Items[0].Title != "Scarf" {
			t.Fatalf("%s: %+v", s, v.Receiver)
		}
	}
}

func TestItemValidation(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	tok := tokenOf(t, st, "Maciej")
	bad := []struct {
		in  ItemInput
		key string
	}{
		{ItemInput{Title: "  "}, "item.title_required"},
		{ItemInput{Title: strings.Repeat("ż", 201)}, "item.title_too_long"},
		{ItemInput{Title: "a", Note: strings.Repeat("n", 501)}, "item.note_too_long"},
		{ItemInput{Title: "a", Link: "javascript:alert(1)"}, "item.link_invalid"},
		{ItemInput{Title: "a", Link: "data:text/html,x"}, "item.link_invalid"},
		{ItemInput{Title: "a", Link: "ftp://x"}, "item.link_invalid"},
		{ItemInput{Title: "a", Link: "https://"}, "item.link_invalid"},
		{ItemInput{Title: "a", Link: "https://x.test/" + strings.Repeat("a", 500)}, "item.link_invalid"},
	}
	for _, c := range bad {
		err := r.AddItem(ctx, tok, c.in)
		var ie *ItemError
		if !errors.Is(err, ErrInvalidItem) || !errors.As(err, &ie) || ie.Key != c.key {
			t.Errorf("%+v: got %v", c.in, err)
		}
	}
	if err := r.AddItem(ctx, tok, ItemInput{Title: strings.Repeat("ż", 200)}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddItem(ctx, tok, ItemInput{Title: " Gift ", Link: "https://allegro.pl/x"}); err != nil {
		t.Fatal(err)
	}
	v, _ := r.PersonView(ctx, tok)
	if len(v.Items) != 2 || v.Items[1].Title != "Gift" {
		t.Fatalf("%+v", v.Items)
	}
}

func TestEditOtherPersonsItem(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := newTestRound(t, defaultYAML)
	mustSync(t, r)
	ania := tokenOf(t, st, "Ania")
	if err := r.AddItem(ctx, ania, ItemInput{Title: "Hat"}); err != nil {
		t.Fatal(err)
	}
	v, _ := r.PersonView(ctx, ania)
	id := v.Items[0].ID
	me := tokenOf(t, st, "Maciej")
	if err := r.UpdateItem(ctx, me, id, ItemInput{Title: "Mine"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update: %v", err)
	}
	if err := r.DeleteItem(ctx, me, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	if err := r.UpdateItem(ctx, ania, id, ItemInput{Title: "Cap"}); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteItem(ctx, ania, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PersonView(ctx, "NOPE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}
