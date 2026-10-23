package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPeopleCRUD(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, err := s.AddPerson(ctx, "Ania", "+48600100200", "TOKENAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPerson(ctx, "Bartek", "+48600100201", "TOKENBBBBBBBBBBB"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PersonByToken(ctx, "TOKENAAAAAAAAAAA")
	if err != nil || got.ID != a.ID || got.Name != "Ania" || got.Greeted {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.PersonByToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.AddPerson(ctx, "Ania", "+48600100202", "TOKENCCCCCCCCCCC"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := s.UpdatePhone(ctx, a.ID, "+48111222333"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGreeted(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	ps, err := s.People(ctx)
	if err != nil || len(ps) != 2 || ps[0].ID != a.ID || ps[0].Phone != "+48111222333" || !ps[0].Greeted || ps[1].Greeted {
		t.Fatalf("people %+v, %v", ps, err)
	}
}

func TestItemsScopedToPerson(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, _ := s.AddPerson(ctx, "Ania", "1", "TA")
	b, _ := s.AddPerson(ctx, "Bartek", "2", "TB")
	it, err := s.AddItem(ctx, Item{PersonID: a.ID, Title: "Book", Link: "https://x", Note: "n"})
	if err != nil {
		t.Fatal(err)
	}
	it2, _ := s.AddItem(ctx, Item{PersonID: a.ID, Title: "Pen"})
	if it.Position != 1 || it2.Position != 2 {
		t.Fatalf("positions %d %d", it.Position, it2.Position)
	}
	evil := it
	evil.PersonID = b.ID
	evil.Title = "hacked"
	if err := s.UpdateItem(ctx, evil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update: %v", err)
	}
	if err := s.DeleteItem(ctx, b.ID, it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	items, _ := s.Items(ctx, a.ID)
	if len(items) != 2 || items[0].Title != "Book" || items[0].Link != "https://x" {
		t.Fatalf("items %+v", items)
	}
	it.Title = "Novel"
	if err := s.UpdateItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	counts, _ := s.ItemCounts(ctx)
	if counts[a.ID] != 2 || counts[b.ID] != 0 {
		t.Fatalf("counts %v", counts)
	}
	if err := s.DeleteItem(ctx, a.ID, it.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCascades(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, _ := s.AddPerson(ctx, "Ania", "1", "TA")
	b, _ := s.AddPerson(ctx, "Bartek", "2", "TB")
	s.AddItem(ctx, Item{PersonID: a.ID, Title: "Book"})
	if err := s.SavePairs(ctx, map[int64]int64{a.ID: b.ID, b.ID: a.ID}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if _, err := s.RecordMessage(ctx, Message{PersonID: a.ID, Kind: "result", ProviderID: 7, Status: "sent", Segments: 2, SentAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePerson(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.Items(ctx, a.ID); len(items) != 0 {
		t.Fatalf("items remain %+v", items)
	}
	if p, _ := s.Pairs(ctx); len(p) != 0 {
		t.Fatalf("pairs remain %v", p)
	}
	ms, err := s.Messages(ctx)
	if err != nil || len(ms) != 1 || ms[0].PersonID != 0 || ms[0].ProviderID != 7 || !ms[0].SentAt.Equal(now) {
		t.Fatalf("messages %+v, %v", ms, err)
	}
}

func TestTxRollback(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	boom := errors.New("boom")
	err := s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.AddPerson(ctx, "Ania", "1", "TA"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if ps, _ := s.People(ctx); len(ps) != 0 {
		t.Fatalf("person survived rollback: %+v", ps)
	}
	if err := s.Tx(ctx, func(tx *Store) error {
		_, err := tx.AddPerson(ctx, "Ania", "1", "TA")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.People(ctx); len(ps) != 1 {
		t.Fatalf("commit lost: %+v", ps)
	}
}

func TestMetaAndReset(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, ok, err := s.Meta(ctx, "k"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	s.SetMeta(ctx, "k", "v1")
	s.SetMeta(ctx, "k", "v2")
	if v, ok, _ := s.Meta(ctx, "k"); !ok || v != "v2" {
		t.Fatalf("meta %q %v", v, ok)
	}
	a, _ := s.AddPerson(ctx, "Ania", "1", "TA")
	b, _ := s.AddPerson(ctx, "Bartek", "2", "TB")
	s.AddItem(ctx, Item{PersonID: a.ID, Title: "x"})
	s.SavePairs(ctx, map[int64]int64{a.ID: b.ID})
	m, _ := s.RecordMessage(ctx, Message{Kind: "test", SentAt: time.Now()})
	if err := s.UpdateMessageStatus(ctx, m.ID, "delivered"); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Messages(ctx); ms[0].Status != "delivered" || ms[0].PersonID != 0 {
		t.Fatalf("%+v", ms)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.People(ctx)
	pr, _ := s.Pairs(ctx)
	ms, _ := s.Messages(ctx)
	cnt, _ := s.ItemCounts(ctx)
	_, ok, _ := s.Meta(ctx, "k")
	if len(ps)+len(pr)+len(ms)+len(cnt) != 0 || ok {
		t.Fatal("reset left rows")
	}
	if _, err := s.AddPerson(ctx, "Ania", "1", "TA"); err != nil {
		t.Fatalf("schema gone after reset: %v", err)
	}
}

func TestReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.AddPerson(ctx, "Ania", "1", "TA")
	s.SetMeta(ctx, "state", "collecting")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := s.PersonByToken(ctx, "TA"); err != nil || p.Name != "Ania" {
		t.Fatalf("%+v %v", p, err)
	}
	if v, ok, _ := s.Meta(ctx, "state"); !ok || v != "collecting" {
		t.Fatal("meta lost")
	}
}
