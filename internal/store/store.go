// Package store is the SQLite persistence layer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Person struct {
	ID                 int64
	Name, Phone, Token string
	Greeted            bool
}

type Item struct {
	ID, PersonID      int64
	Position          int
	Title, Link, Note string
}

type Message struct {
	ID, PersonID int64 // PersonID 0 = none (SMS test, or person since deleted)
	Kind         string
	ProviderID   int64
	Status       string
	Segments     int
	SentAt       time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS people (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	phone TEXT NOT NULL,
	token TEXT NOT NULL UNIQUE,
	greeted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	title TEXT NOT NULL,
	link TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS pairs (
	giver_id INTEGER PRIMARY KEY REFERENCES people(id) ON DELETE CASCADE,
	receiver_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	person_id INTEGER REFERENCES people(id) ON DELETE SET NULL,
	kind TEXT NOT NULL,
	provider_id INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT '',
	segments INTEGER NOT NULL DEFAULT 0,
	sent_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// dbtx is satisfied by both *sql.DB and *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db *sql.DB // nil on a transaction-bound Store
	q  dbtx
}

// Open opens (creating if needed) the database at path and ensures the schema.
// Pragmas are set in the DSN so every pooled connection gets them.
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, q: db}, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Tx runs fn in a transaction; fn gets a Store bound to it. Rolls back on error.
func (s *Store) Tx(ctx context.Context, fn func(*Store) error) (err error) {
	if s.db == nil {
		return errors.New("store: nested transaction")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Store{q: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Meta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id,name,phone,token,greeted FROM people ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Phone, &p.Token, &p.Greeted); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PersonByToken(ctx context.Context, token string) (Person, error) {
	var p Person
	err := s.q.QueryRowContext(ctx, `SELECT id,name,phone,token,greeted FROM people WHERE token=?`, token).
		Scan(&p.ID, &p.Name, &p.Phone, &p.Token, &p.Greeted)
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	return p, err
}

func (s *Store) AddPerson(ctx context.Context, name, phone, token string) (Person, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO people(name,phone,token) VALUES(?,?,?)`, name, phone, token)
	if err != nil {
		return Person{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Person{}, err
	}
	return Person{ID: id, Name: name, Phone: phone, Token: token}, nil
}

func (s *Store) exec1(ctx context.Context, query string, args ...any) error {
	res, err := s.q.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdatePhone(ctx context.Context, id int64, phone string) error {
	return s.exec1(ctx, `UPDATE people SET phone=? WHERE id=?`, phone, id)
}

func (s *Store) SetGreeted(ctx context.Context, id int64, greeted bool) error {
	g := 0
	if greeted {
		g = 1
	}
	return s.exec1(ctx, `UPDATE people SET greeted=? WHERE id=?`, g, id)
}

// DeletePerson cascades items and pairs; messages.person_id becomes NULL.
func (s *Store) DeletePerson(ctx context.Context, id int64) error {
	return s.exec1(ctx, `DELETE FROM people WHERE id=?`, id)
}

func (s *Store) Items(ctx context.Context, personID int64) ([]Item, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id,person_id,position,title,link,note FROM items WHERE person_id=? ORDER BY position,id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.PersonID, &it.Position, &it.Title, &it.Link, &it.Note); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) ItemCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT person_id,COUNT(*) FROM items GROUP BY person_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AddItem appends the item at position max+1 for its person.
func (s *Store) AddItem(ctx context.Context, it Item) (Item, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO items(person_id,position,title,link,note)
		 VALUES(?, (SELECT COALESCE(MAX(position),0)+1 FROM items WHERE person_id=?), ?,?,?)`,
		it.PersonID, it.PersonID, it.Title, it.Link, it.Note)
	if err != nil {
		return Item{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Item{}, err
	}
	err = s.q.QueryRowContext(ctx,
		`SELECT id,person_id,position,title,link,note FROM items WHERE id=?`, id).
		Scan(&it.ID, &it.PersonID, &it.Position, &it.Title, &it.Link, &it.Note)
	return it, err
}

// UpdateItem is scoped to the item's owner; ErrNotFound if no row matches.
func (s *Store) UpdateItem(ctx context.Context, it Item) error {
	return s.exec1(ctx, `UPDATE items SET title=?,link=?,note=? WHERE id=? AND person_id=?`,
		it.Title, it.Link, it.Note, it.ID, it.PersonID)
}

func (s *Store) DeleteItem(ctx context.Context, personID, itemID int64) error {
	return s.exec1(ctx, `DELETE FROM items WHERE id=? AND person_id=?`, itemID, personID)
}

// SavePairs replaces all pairs (giver -> receiver) atomically.
func (s *Store) SavePairs(ctx context.Context, pairs map[int64]int64) error {
	if s.db == nil {
		return s.savePairs(ctx, pairs)
	}
	return s.Tx(ctx, func(tx *Store) error { return tx.savePairs(ctx, pairs) })
}

func (s *Store) savePairs(ctx context.Context, pairs map[int64]int64) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM pairs`); err != nil {
		return err
	}
	for g, r := range pairs {
		if _, err := s.q.ExecContext(ctx, `INSERT INTO pairs(giver_id,receiver_id) VALUES(?,?)`, g, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Pairs(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT giver_id,receiver_id FROM pairs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var g, r int64
		if err := rows.Scan(&g, &r); err != nil {
			return nil, err
		}
		out[g] = r
	}
	return out, rows.Err()
}

func (s *Store) ClearPairs(ctx context.Context) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM pairs`)
	return err
}

func (s *Store) RecordMessage(ctx context.Context, m Message) (Message, error) {
	var pid any
	if m.PersonID != 0 {
		pid = m.PersonID
	}
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO messages(person_id,kind,provider_id,status,segments,sent_at) VALUES(?,?,?,?,?,?)`,
		pid, m.Kind, m.ProviderID, m.Status, m.Segments, m.SentAt.UTC().Format(time.RFC3339))
	if err != nil {
		return Message{}, err
	}
	m.ID, err = res.LastInsertId()
	return m, err
}

// Messages returns all messages, newest first.
func (s *Store) Messages(ctx context.Context) ([]Message, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id,person_id,kind,provider_id,status,segments,sent_at FROM messages ORDER BY sent_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var pid sql.NullInt64
		var at string
		if err := rows.Scan(&m.ID, &pid, &m.Kind, &m.ProviderID, &m.Status, &m.Segments, &at); err != nil {
			return nil, err
		}
		m.PersonID = pid.Int64
		if m.SentAt, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) UpdateMessageStatus(ctx context.Context, id int64, status string) error {
	return s.exec1(ctx, `UPDATE messages SET status=? WHERE id=?`, status, id)
}

// Reset deletes all rows from all tables, keeping the schema.
func (s *Store) Reset(ctx context.Context) error {
	run := func(tx *Store) error {
		for _, t := range []string{"messages", "pairs", "items", "people", "meta"} {
			if _, err := tx.q.ExecContext(ctx, "DELETE FROM "+t); err != nil {
				return err
			}
		}
		return nil
	}
	if s.db == nil {
		return run(s)
	}
	return s.Tx(ctx, run)
}
