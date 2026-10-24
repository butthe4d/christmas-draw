package round

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const (
	kindResult          = "result"
	kindEmergency       = "emergency"
	emergencyMaxSegment = 2
)

// DrawOutcome reports a draw attempt. With Problems the draw did not happen.
type DrawOutcome struct {
	DryRun   bool
	Problems []locale.Msg
	LogPath  string // set when DryRun
}

// Pair is one giver and the person they draw. Only Unseal returns these.
type Pair struct{ Giver, Receiver string }

// resultBatch builds the result SMS for the stored pairs (giver ID to
// receiver ID) from the current items. only != 0 limits it to that giver.
func (r *Round) resultBatch(ctx context.Context, people []store.Person, pairs map[int64]int64, only int64) ([]outgoing, []locale.Msg, error) {
	byID := map[int64]store.Person{}
	for _, p := range people {
		byID[p.ID] = p
	}
	var batch []outgoing
	var problems []locale.Msg
	seen := map[string]bool{}
	for _, giver := range people {
		rid, ok := pairs[giver.ID]
		if !ok || (only != 0 && giver.ID != only) {
			continue
		}
		recv, ok := byID[rid]
		if !ok {
			return nil, nil, locale.M("draw.person_missing")
		}
		items, err := r.st.Items(ctx, recv.ID)
		if err != nil {
			return nil, nil, err
		}
		titles := make([]string, len(items))
		for i, it := range items {
			titles[i] = it.Title
		}
		render := func(token string) (string, error) {
			return message.Result(r.cfg.ResultTemplate, giver.Name, recv.Name,
				message.Link(r.cfg.BaseURL, token), titles, r.cfg.MaxResultSegments)
		}
		text, err := render(giver.Token)
		var masked string
		if err == nil {
			masked, err = render(message.MaskedToken)
		}
		if err != nil {
			m := MessageProblem(kindResult, err)
			if k := m.In("en"); !seen[k] {
				seen[k] = true
				problems = append(problems, m)
			}
			continue
		}
		batch = append(batch, outgoing{personID: giver.ID, sms: sender.SMS{To: giver.Phone, Text: text}, masked: masked})
	}
	return batch, problems, nil
}

// Draw makes a new sealed draw (Collecting, or Drawn to redo it). All
// problems are returned together; with any, nothing changes.
func (r *Round) Draw(ctx context.Context) (DrawOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Collecting, Drawn); err != nil {
		return DrawOutcome{}, err
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return DrawOutcome{}, err
	}
	empty, err := r.withoutItems(ctx)
	if err != nil {
		return DrawOutcome{}, err
	}
	var problems []locale.Msg
	for _, p := range empty {
		problems = append(problems, locale.M("draw.empty_list", "name", p.Name))
	}
	rules := r.rules()
	if m := InfeasibleMsg(draw.Feasible(rules)); m != nil {
		return DrawOutcome{Problems: append(problems, *m)}, nil
	}
	a, err := draw.Draw(rules, r.opts.Rand)
	if err != nil {
		return DrawOutcome{}, err
	}
	if err := draw.Check(rules, a); err != nil {
		// The detail may name pairs: keep it out of the message and the logs.
		return DrawOutcome{}, locale.M("draw.invariant")
	}
	ids := map[string]int64{}
	for _, p := range people {
		ids[p.Name] = p.ID
	}
	pairs := map[int64]int64{}
	for g, rc := range a {
		gid, ok1 := ids[g]
		rid, ok2 := ids[rc]
		if !ok1 || !ok2 {
			return DrawOutcome{}, locale.M("draw.person_missing")
		}
		pairs[gid] = rid
	}
	batch, msgProblems, err := r.resultBatch(ctx, people, pairs, 0)
	if err != nil {
		return DrawOutcome{}, err
	}
	if problems = append(problems, msgProblems...); len(problems) > 0 {
		return DrawOutcome{Problems: problems}, nil
	}
	if r.cfg.DryRun {
		out, err := r.send(ctx, kindResult, batch)
		return DrawOutcome{DryRun: true, LogPath: out.LogPath}, err
	}
	err = r.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.SavePairs(ctx, pairs); err != nil {
			return err
		}
		if err := tx.SetMeta(ctx, metaNoticePending, ""); err != nil {
			return err
		}
		return tx.SetMeta(ctx, metaState, string(Drawn))
	})
	return DrawOutcome{}, err
}

// PreviewResults counts the result SMS. It never returns their content.
func (r *Round) PreviewResults(ctx context.Context) (Preview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Drawn); err != nil {
		return Preview{}, err
	}
	batch, problems, err := r.storedResults(ctx, 0)
	if err != nil {
		return Preview{}, err
	}
	p := r.preview(ctx, batch, problems)
	p.Sample = ""
	return p, nil
}

func (r *Round) storedResults(ctx context.Context, only int64) ([]outgoing, []locale.Msg, error) {
	people, err := r.st.People(ctx)
	if err != nil {
		return nil, nil, err
	}
	pairs, err := r.st.Pairs(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(pairs) == 0 {
		return nil, nil, locale.M("draw.no_pairs")
	}
	return r.resultBatch(ctx, people, pairs, only)
}

// SendResults sends every giver their result and moves Drawn to Delivered.
func (r *Round) SendResults(ctx context.Context) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Drawn); err != nil {
		return Outcome{}, err
	}
	ctx, cancel := detach(ctx)
	defer cancel()
	batch, problems, err := r.storedResults(ctx, 0)
	if err != nil {
		return Outcome{}, err
	}
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	out, err := r.send(ctx, kindResult, batch)
	if out.DryRun || out.Sent == 0 {
		return out, err
	}
	// Even if recording failed, the results are out: the draw is delivered.
	if serr := r.st.SetMeta(ctx, metaState, string(Delivered)); err == nil {
		err = serr
	}
	return out, err
}

// ResendResult sends one person their result again. The text is never returned.
func (r *Round) ResendResult(ctx context.Context, personID int64) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Delivered, ChangePending); err != nil {
		return Outcome{}, err
	}
	ctx, cancel := detach(ctx)
	defer cancel()
	batch, problems, err := r.storedResults(ctx, personID)
	if err != nil {
		return Outcome{}, err
	}
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	if len(batch) == 0 {
		return Outcome{}, store.ErrNotFound
	}
	return r.send(ctx, kindResult, batch)
}

// Unseal returns all pairs sorted by giver and records the first view time.
func (r *Round) Unseal(ctx context.Context) ([]Pair, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Drawn, Delivered, ChangePending); err != nil {
		return nil, err
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return nil, err
	}
	pairs, err := r.st.Pairs(ctx)
	if err != nil {
		return nil, err
	}
	name := map[int64]string{}
	for _, p := range people {
		name[p.ID] = p.Name
	}
	out := make([]Pair, 0, len(pairs))
	for g, rc := range pairs {
		out = append(out, Pair{Giver: name[g], Receiver: name[rc]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Giver < out[j].Giver })
	if _, ok, err := r.st.Meta(ctx, metaUnsealedAt); err != nil {
		return nil, err
	} else if !ok {
		if err := r.st.SetMeta(ctx, metaUnsealedAt, r.opts.Now().Format(time.RFC3339)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// changePlan is what ConfirmChange would send, built from the current config.
type changePlan struct {
	diff      Diff
	people    []store.Person // stored people, before the change
	text      string         // the emergency template in use
	stayingID []int64        // people who stay: the emergency recipients
	emergency []outgoing     // to everyone who stays
	greetings []outgoing     // to newcomers, with a masked link (validation, preview, dry-run)
	problems  []locale.Msg
}

func (r *Round) planChange(ctx context.Context, text string) (changePlan, error) {
	var pl changePlan
	if strings.TrimSpace(text) == "" {
		text = r.cfg.EmergencyTemplate
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return pl, err
	}
	oldHash, haveHash, err := r.st.Meta(ctx, metaRulesHash)
	if err != nil {
		return pl, err
	}
	pl.people = people
	pl.text = text
	pl.diff = r.diff(people, oldHash, haveHash, r.rulesHash())

	removed := map[string]bool{}
	for _, n := range pl.diff.Removed {
		removed[n] = true
	}
	var staying []store.Person
	for _, p := range people {
		if !removed[p.Name] {
			staying = append(staying, p)
			pl.stayingID = append(pl.stayingID, p.ID)
		}
	}
	added := map[string]bool{}
	for _, n := range pl.diff.Added {
		added[n] = true
	}
	var newcomers []store.Person
	for _, cp := range r.cfg.People {
		if added[cp.Name] {
			newcomers = append(newcomers, store.Person{Name: cp.Name, Phone: cp.Phone, Token: message.MaskedToken})
		}
	}
	if m := InfeasibleMsg(draw.Feasible(r.rules())); m != nil {
		pl.problems = append(pl.problems, *m)
	}
	var p1, p2 []locale.Msg
	pl.emergency, p1 = r.build(kindEmergency, text, emergencyMaxSegment, staying)
	pl.greetings, p2 = r.build(kindGreeting, r.cfg.Greeting, greetingMaxSegments, newcomers)
	pl.problems = append(pl.problems, p1...)
	pl.problems = append(pl.problems, p2...)
	return pl, nil
}

// PreviewEmergency checks the emergency text for everyone who stays, plus the
// greeting for newcomers.
func (r *Round) PreviewEmergency(ctx context.Context, text string) (Preview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, ChangePending); err != nil {
		return Preview{}, err
	}
	pl, err := r.planChange(ctx, text)
	if err != nil {
		return Preview{}, err
	}
	batch := append(append([]outgoing{}, pl.emergency...), pl.greetings...)
	return r.preview(ctx, batch, pl.problems), nil
}

// ConfirmChange applies the config change, discards the old draw and sends
// the emergency text to everyone who stays and a greeting to newcomers. In
// dry-run it only writes the log.
func (r *Round) ConfirmChange(ctx context.Context, text string) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, ChangePending); err != nil {
		return Outcome{}, err
	}
	ctx, cancel := detach(ctx)
	defer cancel()
	pl, err := r.planChange(ctx, text)
	if err != nil {
		return Outcome{}, err
	}
	if err := batchError(pl.problems); err != nil {
		return Outcome{}, err
	}
	if r.cfg.DryRun {
		out, err := r.send(ctx, kindEmergency, pl.emergency)
		if err != nil {
			return out, err
		}
		gout, err := r.send(ctx, kindGreeting, pl.greetings)
		return mergeOutcomes(out, gout), err
	}

	err = r.st.Tx(ctx, func(tx *store.Store) error {
		used := map[string]bool{}
		for _, p := range pl.people {
			used[p.Token] = true
		}
		if admin, ok, err := tx.Meta(ctx, metaAdminToken); err != nil {
			return err
		} else if ok {
			used[admin] = true
		}
		if err := r.applyStructure(ctx, tx, pl.people, pl.diff, used, r.rulesHash()); err != nil {
			return err
		}
		if err := tx.ClearPairs(ctx); err != nil {
			return err
		}
		ids := make([]string, len(pl.stayingID))
		for i, id := range pl.stayingID {
			ids[i] = strconv.FormatInt(id, 10)
		}
		for k, v := range map[string]string{
			metaNoticePending: "1", metaNoticeText: pl.text, metaNoticeIDs: strings.Join(ids, ","),
		} {
			if err := tx.SetMeta(ctx, k, v); err != nil {
				return err
			}
		}
		return tx.SetMeta(ctx, metaState, string(Collecting))
	})
	if err != nil {
		return Outcome{}, err
	}

	// Committed: now the texts. Newcomers have real tokens only now.
	out, sendErr := r.send(ctx, kindEmergency, pl.emergency)
	people, err := r.st.People(ctx)
	if err != nil {
		return out, errors.Join(sendErr, err)
	}
	added := map[string]bool{}
	for _, n := range pl.diff.Added {
		added[n] = true
	}
	var newcomers []store.Person
	for _, p := range people {
		if added[p.Name] {
			newcomers = append(newcomers, p)
		}
	}
	greetings, _ := r.build(kindGreeting, r.cfg.Greeting, greetingMaxSegments, newcomers) // validated in planChange
	gout, gerr := r.send(ctx, kindGreeting, greetings)
	if gout.Sent > 0 {
		if merr := r.markGreeted(ctx, gout.sent, Collecting); gerr == nil {
			gerr = merr
		}
	}
	return mergeOutcomes(out, gout), errors.Join(sendErr, gerr)
}

func mergeOutcomes(a, b Outcome) Outcome {
	path := a.LogPath
	if path == "" {
		path = b.LogPath
	}
	return Outcome{
		DryRun:  a.DryRun || b.DryRun,
		Sent:    a.Sent + b.Sent,
		Failed:  append(append([]string(nil), a.Failed...), b.Failed...),
		LogPath: path,
		sent:    append(append([]int64(nil), a.sent...), b.sent...),
	}
}

// changeNotice reads the open emergency-notice window: the template and the
// IDs of the people who were sent it. ok is false when no window is open.
func (r *Round) changeNotice(ctx context.Context) (text string, ids map[int64]bool, ok bool, err error) {
	v, _, err := r.st.Meta(ctx, metaNoticePending)
	if err != nil || v != "1" {
		return "", nil, false, err
	}
	if text, _, err = r.st.Meta(ctx, metaNoticeText); err != nil {
		return "", nil, false, err
	}
	raw, _, err := r.st.Meta(ctx, metaNoticeIDs)
	if err != nil {
		return "", nil, false, err
	}
	ids = map[int64]bool{}
	for _, f := range strings.Split(raw, ",") {
		if id, perr := strconv.ParseInt(f, 10, 64); perr == nil {
			ids[id] = true
		}
	}
	return text, ids, true, nil
}

// ResendChangeNotice sends the confirmed "setup changed" text again to one
// person who stayed, e.g. after the gateway rejected it. It is allowed in
// Collecting until the next successful Draw. Newcomers (who got a greeting
// instead) are not found.
func (r *Round) ResendChangeNotice(ctx context.Context, personID int64) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Collecting); err != nil {
		return Outcome{}, err
	}
	ctx, cancel := detach(ctx)
	defer cancel()
	text, ids, ok, err := r.changeNotice(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, ErrWrongState
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return Outcome{}, err
	}
	var one []store.Person
	for _, p := range people {
		if p.ID == personID && ids[p.ID] {
			one = append(one, p)
		}
	}
	if len(one) == 0 {
		return Outcome{}, store.ErrNotFound
	}
	batch, problems := r.build(kindEmergency, text, emergencyMaxSegment, one)
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	return r.send(ctx, kindEmergency, batch)
}
