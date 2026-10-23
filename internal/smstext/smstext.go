// Package smstext implements GSM-7 detection, length and segment counting,
// and best-effort transliteration of text into the GSM-7 alphabet.
package smstext

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// GSM 03.38 basic table (newline and carriage return included).
const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

// GSM 03.38 extension table; each character costs two units.
const gsmExt = "\f^{}\\[~]|€"

var (
	basicSet = runeSet(gsmBasic)
	extSet   = runeSet(gsmExt)
)

func runeSet(s string) map[rune]bool {
	m := map[rune]bool{}
	for _, r := range s {
		m[r] = true
	}
	return m
}

func encodable(r rune) bool { return basicSet[r] || extSet[r] }

var replacer = strings.NewReplacer(
	"“", `"`, "”", `"`, "„", `"`, "«", `"`, "»", `"`,
	"‘", "'", "’", "'", "‚", "'",
	"–", "-", "—", "-",
	"…", "...",
	"ł", "l", "Ł", "L", "đ", "d", "Đ", "D",
)

// Transliterate replaces typographic punctuation and accented letters that
// GSM-7 cannot encode with plain equivalents. The input is NFC-normalized
// first, so decomposed letters (a + combining ogonek) behave like composed ones. Characters already encodable,
// or with no known equivalent, are left unchanged.
func Transliterate(s string) string {
	s = replacer.Replace(norm.NFC.String(s))
	var b strings.Builder
	for _, r := range s {
		if encodable(r) {
			b.WriteRune(r)
			continue
		}
		var base []rune
		for _, d := range norm.NFD.String(string(r)) {
			if d < 0x300 || d > 0x36F { // skip combining marks
				base = append(base, d)
			}
		}
		if len(base) > 0 && allEncodable(base) {
			b.WriteString(string(base))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func allEncodable(rs []rune) bool {
	for _, r := range rs {
		if !encodable(r) {
			return false
		}
	}
	return true
}

// NonGSM returns the runes of s that GSM-7 cannot encode, in order of first
// appearance, without duplicates.
func NonGSM(s string) []rune {
	var out []rune
	seen := map[rune]bool{}
	for _, r := range s {
		if !encodable(r) && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Length returns the GSM-7 length in units; extension characters count 2.
func Length(s string) int {
	n := 0
	for _, r := range s {
		if extSet[r] {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Segments returns the number of SMS parts needed: 0 for empty text, 1 up to
// 160 units, otherwise ceil(length/153).
func Segments(s string) int {
	n := Length(s)
	switch {
	case n == 0:
		return 0
	case n <= 160:
		return 1
	}
	return (n + 152) / 153
}

// StripNonGSM removes every rune GSM-7 cannot encode.
func StripNonGSM(s string) string {
	return strings.Map(func(r rune) rune {
		if encodable(r) {
			return r
		}
		return -1
	}, s)
}
