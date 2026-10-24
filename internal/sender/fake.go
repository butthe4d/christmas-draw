package sender

import (
	"context"
	"errors"
	"sync"

	"github.com/butthe4d/christmas-draw/internal/smstext"
)

// Fake is an in-memory Sender for tests. It is safe for concurrent use; tests
// read the exported fields directly once the calls have completed.
type Fake struct {
	Sent        []SMS
	FailTo      map[string]bool // Send to these numbers returns an error
	PriceErr    error
	Statuses    map[int64]string
	StatusCalls int // incremented on every Status call

	mu     sync.Mutex
	nextID int64
}

func (f *Fake) Send(_ context.Context, m SMS) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailTo[m.To] {
		return Result{}, errors.New("fake: gateway rejected " + m.To)
	}
	f.nextID++
	f.Sent = append(f.Sent, m)
	return Result{ProviderID: f.nextID, Status: "QUEUED"}, nil
}

// Price returns PriceErr, or 0.14 PLN per segment.
func (f *Fake) Price(_ context.Context, ms []SMS) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PriceErr != nil {
		return 0, f.PriceErr
	}
	segs := 0
	for _, m := range ms {
		segs += smstext.Segments(m.Text)
	}
	return 0.14 * float64(segs), nil
}

// Status returns the entries of Statuses for the requested IDs.
func (f *Fake) Status(_ context.Context, ids []int64) (map[int64]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StatusCalls++
	out := make(map[int64]string, len(ids))
	for _, id := range ids {
		if s, ok := f.Statuses[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}
