package sender

import (
	"context"
	"errors"
	"fmt"
	"math"

	v4 "github.com/gsmservice-pl/messaging-sdk-go/v4"
	"github.com/gsmservice-pl/messaging-sdk-go/v4/models/components"
	"github.com/gsmservice-pl/messaging-sdk-go/v4/models/operations"
)

const (
	priceBatch  = 100 // gateway limit for GetPrice
	statusBatch = 50  // gateway limit for GetByIds

	// statusRejected is the status_code the gateway returns (with HTTP 200)
	// for a message it refused; status_description holds the reason. See
	// https://api.szybkisms.pl/problems/message-statuses/.
	statusRejected = "REJECTED"
)

type gsmService struct {
	client *v4.Client
	sender string
	err    error // set when the configuration is unusable
}

// NewGSMService returns a Sender backed by the gsmservice (SzybkiSMS) API.
// server is "prod" or "sandbox"; senderName is omitted from requests if empty.
// An unknown server makes every call return an error.
func NewGSMService(token, senderName, server string) Sender {
	if _, ok := v4.ServerList[server]; !ok {
		return &gsmService{err: fmt.Errorf("sender: unknown gsmservice server %q", server)}
	}
	return &gsmService{
		client: v4.New(v4.WithSecurity(token), v4.WithServer(server)),
		sender: senderName,
	}
}

func (g *gsmService) message(m SMS) components.SmsMessage {
	msg := components.SmsMessage{
		Recipients: components.CreateSmsMessageRecipientsStr(m.To),
		Message:    m.Text,
		Unicode:    v4.Pointer(false),
	}
	if g.sender != "" {
		msg.Sender = v4.Pointer(g.sender)
	}
	return msg
}

func (g *gsmService) Send(ctx context.Context, m SMS) (Result, error) {
	if g.err != nil {
		return Result{}, g.err
	}
	res, err := g.client.Outgoing.Sms.Send(ctx, operations.CreateSendSmsRequestBodySmsMessage(g.message(m)))
	if err != nil {
		return Result{}, err
	}
	if len(res.Messages) != 1 {
		return Result{}, fmt.Errorf("sender: gateway returned %d results for 1 message", len(res.Messages))
	}
	msg := res.Messages[0]
	status := ""
	if msg.StatusCode != nil {
		status = *msg.StatusCode
	}
	if status == statusRejected || msg.ID == nil || *msg.ID == 0 {
		reason := "no reason given"
		if msg.StatusDescription != nil && *msg.StatusDescription != "" {
			reason = *msg.StatusDescription
		}
		return Result{}, fmt.Errorf("sender: message rejected: %s", reason)
	}
	return Result{ProviderID: *msg.ID, Status: status}, nil
}

func (g *gsmService) Price(ctx context.Context, ms []SMS) (float64, error) {
	if len(ms) == 0 {
		return 0, nil
	}
	if g.err != nil {
		return 0, g.err
	}
	var total float64
	for start := 0; start < len(ms); start += priceBatch {
		end := min(start+priceBatch, len(ms))
		batch := make([]components.SmsMessage, 0, end-start)
		for _, m := range ms[start:end] {
			batch = append(batch, g.message(m))
		}
		res, err := g.client.Outgoing.Sms.GetPrice(ctx, operations.CreateGetSmsPriceRequestBodyArrayOfSmsMessage(batch))
		if err != nil {
			return 0, err
		}
		if len(res.Prices) != len(batch) {
			return 0, fmt.Errorf("sender: gateway priced %d of %d messages", len(res.Prices), len(batch))
		}
		for _, p := range res.Prices {
			if p.Error != nil && *p.Error != "" {
				return 0, errors.New("sender: price error: " + *p.Error)
			}
			if p.Price != nil {
				total += float64(*p.Price)
			}
		}
	}
	return math.Round(total*10000) / 10000, nil
}

func (g *gsmService) Status(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if g.err != nil {
		return nil, g.err
	}
	for start := 0; start < len(ids); start += statusBatch {
		end := min(start+statusBatch, len(ids))
		res, err := g.client.Outgoing.GetByIds(ctx, ids[start:end])
		if err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if m.ID != nil && m.StatusCode != nil {
				out[*m.ID] = *m.StatusCode
			}
		}
	}
	return out, nil
}
