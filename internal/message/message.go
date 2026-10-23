// Package message renders SMS templates and builds the result SMS.
package message

import (
	"fmt"
	"strings"

	"github.com/butthe4d/christmas-draw/internal/smstext"
)

// MaskedToken replaces a person token in dry-run logs and previews.
const MaskedToken = "XXXXXXXXXXXXXXXX"

const cutMarker = "..."

// Link builds a person link from the base URL and a token.
func Link(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "/p/" + token
}

// TooLongError means the SMS needs more segments than allowed.
type TooLongError struct{ Segments, Max int }

func (e *TooLongError) Error() string {
	return fmt.Sprintf("SMS needs %d segments, maximum is %d", e.Segments, e.Max)
}

// NotGSMError means the SMS holds characters that cannot be sent in GSM-7.
type NotGSMError struct{ Runes []rune }

func (e *NotGSMError) Error() string {
	return fmt.Sprintf("SMS contains characters outside GSM-7: %q", string(e.Runes))
}

// Render substitutes {key} for each key in vars (single pass; unknown
// placeholders stay), transliterates, and checks the result fits.
func Render(tmpl string, vars map[string]string, maxSegments int) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if end := strings.IndexByte(tmpl[i:], '}'); end > 0 {
				if v, ok := vars[tmpl[i+1:i+end]]; ok {
					b.WriteString(v)
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	out := smstext.Transliterate(b.String())
	if bad := smstext.NonGSM(out); len(bad) > 0 {
		return "", &NotGSMError{Runes: bad}
	}
	if n := smstext.Segments(out); n > maxSegments {
		return "", &TooLongError{Segments: n, Max: maxSegments}
	}
	return out, nil
}

// Result builds the result SMS, trimming the wish summary to fit maxSegments.
func Result(tmpl, name, receiver, link string, titles []string, maxSegments int) (string, error) {
	var clean []string
	for _, t := range titles {
		t = strings.TrimSpace(smstext.StripNonGSM(smstext.Transliterate(t)))
		if t != "" {
			clean = append(clean, t)
		}
	}
	try := func(summary string) (string, error) {
		return Render(tmpl, map[string]string{
			"name": name, "receiver": receiver, "link": link, "summary": summary,
		}, maxSegments)
	}
	fits := func(summary string) (string, bool, error) {
		out, err := try(summary)
		if _, long := err.(*TooLongError); long {
			return "", false, nil
		}
		return out, err == nil, err
	}

	if len(clean) == 0 {
		return try("")
	}
	for k := len(clean); k >= 1; k-- {
		s := strings.Join(clean[:k], ", ")
		if k < len(clean) {
			s += cutMarker
		}
		out, ok, err := fits(s)
		if err != nil {
			return "", err
		}
		if ok {
			return out, nil
		}
	}
	first := []rune(clean[0])
	for n := len(first); n >= 0; n-- {
		if n == len(first) {
			continue // whole first title already failed above
		}
		s := strings.TrimSpace(string(first[:n])) + cutMarker
		out, ok, err := fits(s)
		if err != nil {
			return "", err
		}
		if ok {
			return out, nil
		}
	}
	_, err := try(cutMarker)
	return "", err
}
