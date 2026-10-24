package message

import (
	"errors"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/smstext"
)

func TestLink(t *testing.T) {
	if got := Link("https://x.ts.net/", "AB"); got != "https://x.ts.net/p/AB" {
		t.Fatalf("got %q", got)
	}
}

func TestRender(t *testing.T) {
	got, err := Render("Hej {name}! {link}", map[string]string{"name": "Łucja", "link": "L"}, 1)
	if err != nil || got != "Hej Lucja! L" {
		t.Fatalf("got %q, %v", got, err)
	}
	_, err = Render("Hi 🎄", nil, 1)
	var ng *NotGSMError
	if !errors.As(err, &ng) || len(ng.Runes) != 1 {
		t.Fatalf("want NotGSMError, got %v", err)
	}
	_, err = Render(strings.Repeat("a", 200), nil, 1)
	var tl *TooLongError
	if !errors.As(err, &tl) || tl.Segments != 2 || tl.Max != 1 {
		t.Fatalf("want TooLongError{2,1}, got %v", err)
	}
}

func TestRenderSinglePass(t *testing.T) {
	got, err := Render("{a} {b} {c}", map[string]string{"a": "{b}", "b": "x"}, 1)
	if err != nil || got != "{b} x {c}" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func defTmpl() string { return locale.T("en", "sms.result", nil) }

func TestResultFitsAll(t *testing.T) {
	out, err := Result(defTmpl(), "Ann", "Bob", "L", []string{"a", "b", "c"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wishes: a, b, c Full") || strings.Contains(out, "...") {
		t.Fatalf("got %q", out)
	}
}

func TestResultTrims(t *testing.T) {
	var titles []string
	for i := 0; i < 30; i++ {
		titles = append(titles, strings.Repeat(string(rune('a'+i%26)), 20))
	}
	link := "https://x.ts.net/p/ABCDEFGHIJKLMNOP"
	out, err := Result(defTmpl(), "Ann", "Bob", link, titles, 2)
	if err != nil {
		t.Fatal(err)
	}
	if smstext.Segments(out) > 2 {
		t.Fatalf("too long: %d", smstext.Segments(out))
	}
	if !strings.HasSuffix(out, "... Full list: "+link) {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(out, titles[0]) {
		t.Fatalf("first title missing: %q", out)
	}
}

func TestResultEmojiTitle(t *testing.T) {
	out, err := Result(defTmpl(), "Ann", "Bob", "L", []string{"🎁", "klocki 🎁 lego"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wishes: klocki  lego Full") {
		t.Fatalf("got %q", out)
	}
}

func TestResultHugeSingleTitle(t *testing.T) {
	out, err := Result(defTmpl(), "Ann", "Bob", "L", []string{strings.Repeat("x", 1000)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if smstext.Segments(out) != 1 || !strings.Contains(out, "...") {
		t.Fatalf("got %q", out)
	}
}

func TestResultImpossible(t *testing.T) {
	_, err := Result(defTmpl(), strings.Repeat("n", 400), "Bob", "L", []string{"a"}, 1)
	var tl *TooLongError
	if !errors.As(err, &tl) {
		t.Fatalf("want TooLongError, got %v", err)
	}
}

func TestResultNoTitles(t *testing.T) {
	out, err := Result(defTmpl(), "Ann", "Bob", "L", []string{"🎁"}, 3)
	if err != nil || strings.Contains(out, "...") {
		t.Fatalf("got %q, %v", out, err)
	}
}
