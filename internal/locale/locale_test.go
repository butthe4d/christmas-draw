package locale

import (
	"sort"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/smstext"
)

func TestLocalesComplete(t *testing.T) {
	base := keysOf(catalogs["en"])
	for _, code := range Available() {
		got := catalogs[code]
		var missing, extra []string
		for k := range base {
			if _, ok := got[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range got {
			if _, ok := base[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: missing %v, extra %v", code, missing, extra)
		}
	}
}

func keysOf(m map[string]string) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func TestAvailable(t *testing.T) {
	a := strings.Join(Available(), ",")
	if a != "en,pl" {
		t.Fatalf("got %s", a)
	}
	if !Has("pl") || Has("de") {
		t.Fatal("Has")
	}
}

func TestFallback(t *testing.T) {
	if T("de", "sms.result", nil) != T("en", "sms.result", nil) {
		t.Fatal("de should fall back to en")
	}
	if T("en", "no.such.key", nil) != "no.such.key" {
		t.Fatal("missing key returns itself")
	}
}

func TestArgs(t *testing.T) {
	got := M("sms.test", "time", "21:03", "code", "4821").In("pl")
	if got != "christmas-draw test 21:03 kod 4821" {
		t.Fatalf("got %q", got)
	}
	var err error = M("sms.emergency")
	if err.Error() != T("en", "sms.emergency", nil) {
		t.Fatal("Error() should be English text")
	}
}

func TestSMSDefaultsAreGSM(t *testing.T) {
	for code, cat := range catalogs {
		for k, v := range cat {
			if strings.HasPrefix(k, "sms.") {
				if bad := smstext.NonGSM(v); len(bad) != 0 {
					t.Errorf("%s %s has non-GSM %q", code, k, bad)
				}
			}
		}
	}
}
