package smstext

import (
	"reflect"
	"strings"
	"testing"
)

func TestTransliterate(t *testing.T) {
	cases := map[string]string{
		"Zażółć gęślą jaźń": "Zazolc gesla jazn",
		"ŁĄKA Ść Ń":         "LAKA Sc N",
		"“cytat” – ok…":     `"cytat" - ok...`,
		"„dolny”":           `"dolny"`,
		"Čeština ő ü é ñ":   "Cestina o ü é ñ",
	}
	for in, want := range cases {
		if got := Transliterate(in); got != want {
			t.Errorf("Transliterate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNonGSM(t *testing.T) {
	if got := NonGSM("Hej {name} €5 [ok]"); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
	if got := NonGSM("prezent 🎁 漢 漢 ü"); !reflect.DeepEqual(got, []rune{'🎁', '漢'}) {
		t.Fatalf("got %q", got)
	}
}

func TestLengthAndSegments(t *testing.T) {
	if Length("a€[") != 5 {
		t.Fatal("extension chars count 2")
	}
	if Segments("") != 0 {
		t.Fatal("empty")
	}
	if Segments(strings.Repeat("a", 160)) != 1 {
		t.Fatal("160 = 1")
	}
	if Segments(strings.Repeat("a", 161)) != 2 {
		t.Fatal("161 = 2")
	}
	if Segments(strings.Repeat("a", 306)) != 2 {
		t.Fatal("306 = 2")
	}
	if Segments(strings.Repeat("a", 307)) != 3 {
		t.Fatal("307 = 3")
	}
}

func TestStripNonGSM(t *testing.T) {
	if got := StripNonGSM("klocki 🎁 lego"); got != "klocki  lego" {
		t.Fatalf("got %q", got)
	}
}

func TestTransliterateNormalizesDecomposedInput(t *testing.T) {
	cases := map[string]string{
		"a\u0328":         "a", // decomposed a-ogonek: no stray combining mark
		"Zo\u0301\u0142w": "Zolw",
		"e\u0301":         "é", // composes to a GSM-7 letter and is kept
		"c\u030c":         "c",
	}
	for in, want := range cases {
		if got := Transliterate(in); got != want {
			t.Errorf("Transliterate(%q) = %q, want %q", in, got, want)
		}
	}
}
