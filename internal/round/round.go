// Package round owns the lifecycle state machine and reconciles the YAML
// config with the database on every start.
package round

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

type State string

const (
	Setup         State = "SETUP"
	Collecting    State = "COLLECTING"
	Drawn         State = "DRAWN"
	Delivered     State = "DELIVERED"
	ChangePending State = "CHANGE_PENDING"
)

var ErrWrongState = errors.New("action not allowed in current state")

// Meta keys.
const (
	metaState      = "state"
	metaAdminToken = "admin_token"
	metaRulesHash  = "rules_hash"
	metaUnsealedAt = "unsealed_at" // RFC 3339; written by later tasks
)

// Options holds injectable dependencies; zero values mean time.Now and crypto/rand.
type Options struct {
	Now  func() time.Time
	Rand io.Reader
}

type Round struct {
	cfg  *config.Config
	st   *store.Store
	snd  sender.Sender
	dry  sender.DryRunLog
	opts Options
	mu   sync.Mutex // held by every exported method; also serializes SQLite writes

	lastRefresh time.Time // last RefreshStatuses gateway call; guarded by mu
}

func New(cfg *config.Config, st *store.Store, snd sender.Sender, dry sender.DryRunLog, o Options) *Round {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	return &Round{cfg: cfg, st: st, snd: snd, dry: dry, opts: o}
}

type Diff struct {
	Added, Removed []string
	PhoneChanged   []string
	RulesChanged   bool
}

// Structural reports changes that affect who is in the draw or how.
func (d Diff) Structural() bool { return len(d.Added)+len(d.Removed) > 0 || d.RulesChanged }

type SyncReport struct {
	Diff              Diff
	NewState          State
	AdminTokenCreated bool
}

// newToken returns 10 random bytes as unpadded base32 (16 chars).
func newToken(rnd io.Reader) (string, error) {
	var b [10]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]), nil
}

// uniqueToken retries until the token is not in used, then reserves it.
func (r *Round) uniqueToken(used map[string]bool) (string, error) {
	for range 100 {
		t, err := newToken(r.opts.Rand)
		if err != nil {
			return "", err
		}
		if !used[t] {
			used[t] = true
			return t, nil
		}
	}
	return "", errors.New("round: cannot generate a unique token")
}

func (r *Round) names() []string {
	names := make([]string, len(r.cfg.People))
	for i, p := range r.cfg.People {
		names[i] = p.Name
	}
	return names
}

// Rules returns the draw rules for the configured group.
func (r *Round) Rules() draw.Rules {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rules()
}

// rules is Rules without locking, for use inside locked methods.
func (r *Round) rules() draw.Rules {
	return draw.NewRules(r.names(), r.cfg.Couples, r.cfg.Exclusions, r.cfg.ExcludeCouples)
}

// rulesHash is order-insensitive: each pair is sorted internally, then the list.
func (r *Round) rulesHash() string {
	enc := func(pairs [][2]string) []string {
		out := make([]string, len(pairs))
		for i, p := range pairs {
			a, b := p[0], p[1]
			if b < a {
				a, b = b, a
			}
			out[i] = fmt.Sprintf("%q,%q", a, b)
		}
		sort.Strings(out)
		return out
	}
	h := sha256.New()
	fmt.Fprintf(h, "couples=%q\nexclusions=%q\nexclude_couples=%t\n",
		enc(r.cfg.Couples), enc(r.cfg.Exclusions), r.cfg.ExcludeCouples)
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Round) State(ctx context.Context) (State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state(ctx, r.st)
}

func (r *Round) state(ctx context.Context, st *store.Store) (State, error) {
	v, ok, err := st.Meta(ctx, metaState)
	if err != nil {
		return "", err
	}
	if !ok {
		return Setup, nil
	}
	return State(v), nil
}

func (r *Round) AdminToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok, err := r.st.Meta(ctx, metaAdminToken)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}

// Sync reconciles the config with the database in one transaction.
func (r *Round) Sync(ctx context.Context) (SyncReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rep SyncReport
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		var err error
		rep, err = r.sync(ctx, tx)
		return err
	})
	if err != nil {
		return SyncReport{}, err
	}
	return rep, nil
}

func (r *Round) sync(ctx context.Context, tx *store.Store) (SyncReport, error) {
	var rep SyncReport
	people, err := tx.People(ctx)
	if err != nil {
		return rep, err
	}
	used := map[string]bool{}
	for _, p := range people {
		used[p.Token] = true
	}

	admin, haveAdmin, err := tx.Meta(ctx, metaAdminToken)
	if err != nil {
		return rep, err
	}
	if haveAdmin {
		used[admin] = true
	} else {
		t, err := r.uniqueToken(used)
		if err != nil {
			return rep, err
		}
		if err := tx.SetMeta(ctx, metaAdminToken, t); err != nil {
			return rep, err
		}
		rep.AdminTokenCreated = true
	}

	stateStr, haveState, err := tx.Meta(ctx, metaState)
	if err != nil {
		return rep, err
	}
	state := State(stateStr)
	if !haveState {
		state = Setup
	}
	newHash := r.rulesHash()
	oldHash, haveHash, err := tx.Meta(ctx, metaRulesHash)
	if err != nil {
		return rep, err
	}

	// Diff by trimmed name.
	byName := map[string]store.Person{}
	for _, p := range people {
		byName[p.Name] = p
	}
	rep.Diff = r.diff(people, oldHash, haveHash, newHash)

	applyPhones := func() error {
		for _, cp := range r.cfg.People {
			if p, ok := byName[cp.Name]; ok && p.Phone != cp.Phone {
				if err := tx.UpdatePhone(ctx, p.ID, cp.Phone); err != nil {
					return err
				}
			}
		}
		return nil
	}
	applyStructure := func() error {
		return r.applyStructure(ctx, tx, people, rep.Diff, used, newHash)
	}

	if err := applyPhones(); err != nil {
		return rep, err
	}
	structural := rep.Diff.Structural()
	switch {
	case !haveState: // empty DB
		if err := applyStructure(); err != nil {
			return rep, err
		}
	case state == Setup || state == Collecting:
		if err := applyStructure(); err != nil {
			return rep, err
		}
	case state == Drawn:
		if structural {
			if err := applyStructure(); err != nil {
				return rep, err
			}
			if err := tx.ClearPairs(ctx); err != nil {
				return rep, err
			}
			state = Collecting
		}
	case state == Delivered:
		if structural {
			state = ChangePending
		}
	case state == ChangePending:
		if !structural {
			state = Delivered
		}
	default:
		return rep, fmt.Errorf("round: unknown state %q", state)
	}
	if !haveHash {
		if err := tx.SetMeta(ctx, metaRulesHash, newHash); err != nil {
			return rep, err
		}
	}
	if err := tx.SetMeta(ctx, metaState, string(state)); err != nil {
		return rep, err
	}
	rep.NewState = state
	return rep, nil
}

// diff compares the config with the stored people and rules hash. Used by
// sync and by the admin overview so both see the same changes.
func (r *Round) diff(people []store.Person, oldHash string, haveHash bool, newHash string) Diff {
	var d Diff
	byName := map[string]store.Person{}
	for _, p := range people {
		byName[p.Name] = p
	}
	inCfg := map[string]bool{}
	for _, cp := range r.cfg.People {
		inCfg[cp.Name] = true
		p, ok := byName[cp.Name]
		switch {
		case !ok:
			d.Added = append(d.Added, cp.Name)
		case p.Phone != cp.Phone:
			d.PhoneChanged = append(d.PhoneChanged, cp.Name)
		}
	}
	for _, p := range people {
		if !inCfg[p.Name] {
			d.Removed = append(d.Removed, p.Name)
		}
	}
	d.RulesChanged = haveHash && oldHash != newHash
	return d
}

// applyStructure deletes removed people, adds newcomers with fresh tokens
// (recorded in used) and stores the new rules hash.
func (r *Round) applyStructure(ctx context.Context, tx *store.Store, people []store.Person, d Diff, used map[string]bool, newHash string) error {
	byName := map[string]store.Person{}
	for _, p := range people {
		byName[p.Name] = p
	}
	for _, name := range d.Removed {
		if err := tx.DeletePerson(ctx, byName[name].ID); err != nil {
			return err
		}
	}
	for _, cp := range r.cfg.People {
		if _, ok := byName[cp.Name]; ok {
			continue
		}
		t, err := r.uniqueToken(used)
		if err != nil {
			return err
		}
		if _, err := tx.AddPerson(ctx, cp.Name, cp.Phone, t); err != nil {
			return err
		}
	}
	return tx.SetMeta(ctx, metaRulesHash, newHash)
}
