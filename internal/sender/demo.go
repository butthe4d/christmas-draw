package sender

import (
	"context"
	"sync"

	"github.com/butthe4d/christmas-draw/internal/smstext"
)

// Demo is the Sender for the demo instance. It never contacts a gateway and
// keeps no message text: every SMS is accepted and reported delivered.
type Demo struct {
	mu     sync.Mutex
	nextID int64
}

func (d *Demo) Send(_ context.Context, _ SMS) (Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return Result{ProviderID: d.nextID, Status: "DELIVERED"}, nil
}

// Price returns 0.14 per segment, the gsmservice price the defaults assume.
func (d *Demo) Price(_ context.Context, ms []SMS) (float64, error) {
	segs := 0
	for _, m := range ms {
		segs += smstext.Segments(m.Text)
	}
	return 0.14 * float64(segs), nil
}

func (d *Demo) Status(_ context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	for _, id := range ids {
		out[id] = "DELIVERED"
	}
	return out, nil
}
