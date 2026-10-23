package round

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/smstext"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const (
	greetingMaxSegments = 2
	reminderMaxSegments = 2
	testMaxSegments     = 1
	statusRefreshEvery  = 30 * time.Second
	statusMaxAge        = 72 * time.Hour
)

// Message kinds recorded in the store.
const (
	kindGreeting = "greeting"
	kindReminder = "reminder"
	kindTest     = "test"
)

type Preview struct {
	Messages, Segments int
	Cost               float64
	CostEstimated      bool         // true when Sender.Price failed and config price was used
	Sample             string       // first message with masked token; "" for results
	Problems           []locale.Msg // blocking problems (template too long, not GSM, infeasible, ...)
}

type Outcome struct {
	DryRun  bool
	Sent    int      // SMS accepted by the gateway; in dry-run, SMS written to the log
	Failed  []string // person names
	LogPath string   // set when DryRun

	sent []int64 // person IDs whose SMS the gateway accepted (not dry-run)
}

type TestResult struct {
	To, Text   string
	ProviderID int64
	Status     string
}

type PersonStatus struct {
	ID                   int64
	Name                 string
	Items                int
	Greeted              bool
	LastKind, LastStatus string
}

type Overview struct {
	State      State
	People     []PersonStatus
	Ungreeted  int
	Infeasible *locale.Msg // nil when a draw exists
	UnsealedAt *time.Time
	Pending    *Diff // non-nil in ChangePending
	DryRun     bool
}

// outgoing is one message of a batch: the real SMS and its masked log text.
type outgoing struct {
	personID int64
	sms      sender.SMS
	masked   string
}

// infeasibleMsg converts a draw.Feasible error into a message.
func infeasibleMsg(err error) *locale.Msg {
	if err == nil {
		return nil
	}
	var ie *draw.InfeasibleError
	if errors.As(err, &ie) {
		var m locale.Msg
		if len(ie.Receivers) == 0 && len(ie.Givers) > 0 {
			m = locale.M("draw.nobody_allowed", "name", ie.Givers[0])
		} else {
			m = locale.M("draw.infeasible",
				"givers", strings.Join(ie.Givers, ", "),
				"receivers", strings.Join(ie.Receivers, ", "))
		}
		return &m
	}
	m := locale.M("draw.error", "error", err.Error())
	return &m
}

// messageProblem converts a message.Render error into a message about the
// SMS of the given kind.
func messageProblem(kind string, err error) locale.Msg {
	var long *message.TooLongError
	var bad *message.NotGSMError
	switch {
	case errors.As(err, &long):
		return locale.M("problem.sms_too_long", "kind", kind,
			"segments", strconv.Itoa(long.Segments), "max", strconv.Itoa(long.Max))
	case errors.As(err, &bad):
		return locale.M("problem.sms_not_gsm", "kind", kind, "chars", string(bad.Runes))
	}
	return locale.M("problem.sms_error", "kind", kind, "error", err.Error())
}

// build renders tmpl for each person, with the real and the masked link.
// Identical problems are reported once.
func (r *Round) build(kind, tmpl string, maxSeg int, people []store.Person) ([]outgoing, []locale.Msg) {
	var batch []outgoing
	var problems []locale.Msg
	seen := map[string]bool{}
	for _, p := range people {
		vars := func(link string) map[string]string {
			return map[string]string{"name": p.Name, "link": link}
		}
		text, err := message.Render(tmpl, vars(message.Link(r.cfg.BaseURL, p.Token)), maxSeg)
		if err == nil {
			var masked string
			masked, err = message.Render(tmpl, vars(message.Link(r.cfg.BaseURL, message.MaskedToken)), maxSeg)
			if err == nil {
				batch = append(batch, outgoing{p.ID, sender.SMS{To: p.Phone, Text: text}, masked})
				continue
			}
		}
		m := messageProblem(kind, err)
		if k := m.In("en"); !seen[k] {
			seen[k] = true
			problems = append(problems, m)
		}
	}
	return batch, problems
}

func smsList(batch []outgoing) []sender.SMS {
	out := make([]sender.SMS, len(batch))
	for i, o := range batch {
		out[i] = o.sms
	}
	return out
}

func (r *Round) preview(ctx context.Context, batch []outgoing, problems []locale.Msg) Preview {
	p := Preview{Messages: len(batch), Problems: problems}
	for _, o := range batch {
		p.Segments += smstext.Segments(o.sms.Text)
	}
	if len(batch) > 0 {
		p.Sample = batch[0].masked
		cost, err := r.snd.Price(ctx, smsList(batch))
		if err != nil {
			cost = r.cfg.SMS.PricePerSegment * float64(p.Segments)
			p.CostEstimated = true
		}
		p.Cost = cost
	}
	return p
}

func (r *Round) ungreeted(ctx context.Context) ([]store.Person, error) {
	all, err := r.st.People(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Person
	for _, p := range all {
		if !p.Greeted {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *Round) withoutItems(ctx context.Context) ([]store.Person, error) {
	all, err := r.st.People(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := r.st.ItemCounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Person
	for _, p := range all {
		if counts[p.ID] == 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

// requireState returns ErrWrongState unless the current state is one of ok.
func (r *Round) requireState(ctx context.Context, ok ...State) (State, error) {
	s, err := r.state(ctx, r.st)
	if err != nil {
		return "", err
	}
	for _, o := range ok {
		if s == o {
			return s, nil
		}
	}
	return s, ErrWrongState
}

// send delivers a validated batch. In dry-run it logs the masked texts and
// changes nothing else. Otherwise it records a message per accepted SMS and
// reports failures by person name; it returns an error only if all fail.
func (r *Round) send(ctx context.Context, kind string, batch []outgoing) (Outcome, error) {
	var out Outcome
	if len(batch) == 0 {
		out.DryRun = r.cfg.DryRun
		return out, nil
	}
	if r.cfg.DryRun {
		logged := make([]sender.SMS, len(batch))
		for i, o := range batch {
			logged[i] = sender.SMS{To: o.sms.To, Text: o.masked}
		}
		if err := r.dry.Write(kind, logged, r.opts.Now()); err != nil {
			return out, err
		}
		return Outcome{DryRun: true, Sent: len(batch), LogPath: r.dry.Path}, nil
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return out, err
	}
	names := map[int64]string{}
	for _, p := range people {
		names[p.ID] = p.Name
	}
	var firstErr error
	for _, o := range batch {
		res, err := r.snd.Send(ctx, o.sms)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			out.Failed = append(out.Failed, names[o.personID])
			continue
		}
		out.Sent++
		out.sent = append(out.sent, o.personID)
		if _, err := r.st.RecordMessage(ctx, store.Message{
			PersonID: o.personID, Kind: kind, ProviderID: res.ProviderID, Status: res.Status,
			Segments: smstext.Segments(o.sms.Text), SentAt: r.opts.Now(),
		}); err != nil {
			// The SMS went out; callers still act on out.sent.
			return out, err
		}
	}
	if out.Sent == 0 {
		return out, locale.M("send.all_failed", "count", strconv.Itoa(len(batch)), "error", firstErr.Error())
	}
	return out, nil
}

// batchError returns the first problem as an error, or nil.
func batchError(problems []locale.Msg) error {
	if len(problems) > 0 {
		return problems[0]
	}
	return nil
}

func (r *Round) PreviewGreetings(ctx context.Context) (Preview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Setup, Collecting); err != nil {
		return Preview{}, err
	}
	people, err := r.ungreeted(ctx)
	if err != nil {
		return Preview{}, err
	}
	batch, problems := r.build(kindGreeting, r.cfg.Greeting, greetingMaxSegments, people)
	return r.preview(ctx, batch, problems), nil
}

func (r *Round) SendGreetings(ctx context.Context) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.requireState(ctx, Setup, Collecting)
	if err != nil {
		return Outcome{}, err
	}
	people, err := r.ungreeted(ctx)
	if err != nil {
		return Outcome{}, err
	}
	batch, problems := r.build(kindGreeting, r.cfg.Greeting, greetingMaxSegments, people)
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	out, err := r.send(ctx, kindGreeting, batch)
	if out.DryRun || out.Sent == 0 {
		return out, err
	}
	// Even if recording failed, those who got the SMS must not be texted again.
	if merr := r.markGreeted(ctx, out.sent, state); err == nil {
		err = merr
	}
	return out, err
}

// markGreeted flags the people whose SMS went out, and moves Setup on to
// Collecting. People deleted in the meantime are skipped.
func (r *Round) markGreeted(ctx context.Context, ids []int64, state State) error {
	return r.st.Tx(ctx, func(tx *store.Store) error {
		for _, id := range ids {
			if err := tx.SetGreeted(ctx, id, true); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if state == Setup {
			return tx.SetMeta(ctx, metaState, string(Collecting))
		}
		return nil
	})
}

func (r *Round) PreviewReminders(ctx context.Context) (Preview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Collecting); err != nil {
		return Preview{}, err
	}
	people, err := r.withoutItems(ctx)
	if err != nil {
		return Preview{}, err
	}
	batch, problems := r.build(kindReminder, r.cfg.ReminderTemplate, reminderMaxSegments, people)
	return r.preview(ctx, batch, problems), nil
}

func (r *Round) SendReminders(ctx context.Context) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireState(ctx, Collecting); err != nil {
		return Outcome{}, err
	}
	people, err := r.withoutItems(ctx)
	if err != nil {
		return Outcome{}, err
	}
	batch, problems := r.build(kindReminder, r.cfg.ReminderTemplate, reminderMaxSegments, people)
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	return r.send(ctx, kindReminder, batch)
}

// ResendGreeting sends the greeting to one person again (any state but Setup).
func (r *Round) ResendGreeting(ctx context.Context, personID int64) (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.requireState(ctx, Collecting, Drawn, Delivered, ChangePending)
	if err != nil {
		return Outcome{}, err
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return Outcome{}, err
	}
	var one []store.Person
	for _, p := range people {
		if p.ID == personID {
			one = append(one, p)
		}
	}
	if len(one) == 0 {
		return Outcome{}, store.ErrNotFound
	}
	batch, problems := r.build(kindGreeting, r.cfg.Greeting, greetingMaxSegments, one)
	if err := batchError(problems); err != nil {
		return Outcome{}, err
	}
	out, err := r.send(ctx, kindGreeting, batch)
	if out.DryRun || out.Sent == 0 {
		return out, err
	}
	if merr := r.markGreeted(ctx, out.sent, state); err == nil {
		err = merr
	}
	return out, err
}

// randCode returns a uniform number in [0, 10000) read from rnd.
func randCode(rnd io.Reader) (int, error) {
	const limit = 4294960000 // largest multiple of 10000 below 2^32
	var b [4]byte
	for {
		if _, err := io.ReadFull(rnd, b[:]); err != nil {
			return 0, err
		}
		n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		if n < limit {
			return int(n % 10000), nil
		}
	}
}

// SendTestSMS texts the organiser. It is sent even in dry-run.
func (r *Round) SendTestSMS(ctx context.Context) (TestResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg.OrganiserPhone == "" {
		return TestResult{}, locale.M("send.no_organiser_phone")
	}
	code, err := randCode(r.opts.Rand)
	if err != nil {
		return TestResult{}, err
	}
	raw := locale.T(r.cfg.Language, "sms.test", map[string]string{
		"time": r.opts.Now().Local().Format("15:04"),
		"code": fmt.Sprintf("%04d", code),
	})
	text, err := message.Render(raw, nil, testMaxSegments)
	if err != nil {
		return TestResult{}, messageProblem(kindTest, err)
	}
	res, err := r.snd.Send(ctx, sender.SMS{To: r.cfg.OrganiserPhone, Text: text})
	if err != nil {
		return TestResult{}, locale.M("send.test_failed", "error", err.Error())
	}
	if _, err := r.st.RecordMessage(ctx, store.Message{
		Kind: kindTest, ProviderID: res.ProviderID, Status: res.Status,
		Segments: smstext.Segments(text), SentAt: r.opts.Now(),
	}); err != nil {
		return TestResult{}, err
	}
	return TestResult{To: r.cfg.OrganiserPhone, Text: text, ProviderID: res.ProviderID, Status: res.Status}, nil
}

// RefreshStatuses asks the gateway for the status of recent messages, at most
// once per 30 seconds.
func (r *Round) RefreshStatuses(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opts.Now()
	if !r.lastRefresh.IsZero() && now.Sub(r.lastRefresh) < statusRefreshEvery {
		return nil
	}
	msgs, err := r.st.Messages(ctx)
	if err != nil {
		return err
	}
	var ids []int64
	byProvider := map[int64][]int64{}
	for _, m := range msgs {
		if m.ProviderID > 0 && now.Sub(m.SentAt) <= statusMaxAge {
			if len(byProvider[m.ProviderID]) == 0 {
				ids = append(ids, m.ProviderID)
			}
			byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	r.lastRefresh = now
	statuses, err := r.snd.Status(ctx, ids)
	if err != nil {
		return err
	}
	for pid, status := range statuses {
		for _, id := range byProvider[pid] {
			if err := r.st.UpdateMessageStatus(ctx, id, status); err != nil {
				return err
			}
		}
	}
	return nil
}

// Overview gathers what the admin page shows.
func (r *Round) Overview(ctx context.Context) (Overview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.state(ctx, r.st)
	if err != nil {
		return Overview{}, err
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return Overview{}, err
	}
	counts, err := r.st.ItemCounts(ctx)
	if err != nil {
		return Overview{}, err
	}
	msgs, err := r.st.Messages(ctx) // newest first
	if err != nil {
		return Overview{}, err
	}
	last := map[int64]store.Message{}
	for _, m := range msgs {
		if _, ok := last[m.PersonID]; !ok {
			last[m.PersonID] = m
		}
	}
	ov := Overview{State: state, DryRun: r.cfg.DryRun, Infeasible: infeasibleMsg(draw.Feasible(r.rules()))}
	for _, p := range people {
		ps := PersonStatus{ID: p.ID, Name: p.Name, Items: counts[p.ID], Greeted: p.Greeted}
		if m, ok := last[p.ID]; ok {
			ps.LastKind, ps.LastStatus = m.Kind, m.Status
		}
		if !p.Greeted {
			ov.Ungreeted++
		}
		ov.People = append(ov.People, ps)
	}
	if v, ok, err := r.st.Meta(ctx, metaUnsealedAt); err != nil {
		return ov, err
	} else if ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			ov.UnsealedAt = &t
		}
	}
	if state == ChangePending {
		old, ok, err := r.st.Meta(ctx, metaRulesHash)
		if err != nil {
			return ov, err
		}
		d := r.diff(people, old, ok, r.rulesHash())
		ov.Pending = &d
	}
	return ov, nil
}
