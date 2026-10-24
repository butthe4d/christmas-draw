//go:build integration

package sender

import (
	"context"
	"os"
	"testing"
)

func TestGSMServiceSandbox(t *testing.T) {
	token := os.Getenv("GSMSERVICE_SANDBOX_TOKEN")
	if token == "" {
		t.Skip("GSMSERVICE_SANDBOX_TOKEN not set")
	}
	s := NewGSMService(token, "", "sandbox")
	ctx := context.Background()
	res, err := s.Send(ctx, SMS{To: "+48600100200", Text: "christmas-draw sandbox test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderID <= 0 {
		t.Fatalf("ProviderID = %d", res.ProviderID)
	}
	st, err := s.Status(ctx, []int64{res.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st[res.ProviderID]; !ok {
		t.Fatalf("status missing id %d: %v", res.ProviderID, st)
	}
}
