// Package sender delivers SMS through a gateway (or logs/fakes it).
package sender

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/butthe4d/christmas-draw/internal/smstext"
)

// SMS is one outgoing message. Text must already be transliterated.
type SMS struct{ To, Text string }

// Result is what the gateway reported for an accepted message.
type Result struct {
	ProviderID int64
	Status     string
}

// Sender is the SMS gateway abstraction.
type Sender interface {
	// Send returns an error if the gateway rejects the message.
	Send(ctx context.Context, m SMS) (Result, error)
	// Price returns the total price in PLN. An empty slice costs nothing.
	Price(ctx context.Context, ms []SMS) (float64, error)
	// Status returns the current status code for each known provider ID.
	Status(ctx context.Context, ids []int64) (map[int64]string, error)
}

// DryRunLog appends what would have been sent to a file.
type DryRunLog struct{ Path string }

// Write appends one block: a header line, then one line per message. The
// caller passes already-masked text. The file is created (0600) if missing.
func (l DryRunLog) Write(kind string, ms []SMS, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s %s (%d SMS) ===\n", now.Format("2006-01-02 15:04:05"), kind, len(ms))
	for _, m := range ms {
		fmt.Fprintf(&b, "%s [%d seg] %s\n", m.To, smstext.Segments(m.Text), m.Text)
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
