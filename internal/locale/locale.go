// Package locale provides embedded, flat-JSON translations with fallback to English.
package locale

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

const fallback = "en"

// catalogs maps language code to key -> text.
var catalogs = load()

func load() map[string]map[string]string {
	entries, err := files.ReadDir("locales")
	if err != nil {
		panic(err)
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		data, err := files.ReadFile(path.Join("locales", e.Name()))
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			panic(fmt.Sprintf("locale %s: %v", e.Name(), err))
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	return out
}

// Msg is a translatable message: a key plus placeholder values.
type Msg struct {
	Key  string
	Args map[string]string
}

// M builds a Msg from alternating placeholder names and values.
func M(key string, kv ...string) Msg {
	m := Msg{Key: key}
	if len(kv) > 0 {
		m.Args = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m.Args[kv[i]] = kv[i+1]
		}
	}
	return m
}

// Available returns the sorted codes of the embedded languages.
func Available() []string {
	codes := make([]string, 0, len(catalogs))
	for c := range catalogs {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// Has reports whether a language is embedded.
func Has(lang string) bool {
	_, ok := catalogs[lang]
	return ok
}

// T looks the key up in lang, then in English, then returns the key itself,
// substituting {arg} placeholders.
func T(lang, key string, args map[string]string) string {
	text, ok := catalogs[lang][key]
	if !ok {
		if text, ok = catalogs[fallback][key]; !ok {
			text = key
		}
	}
	if len(args) == 0 {
		return text
	}
	pairs := make([]string, 0, len(args)*2)
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// In renders the message in the given language.
func (m Msg) In(lang string) string { return T(lang, m.Key, m.Args) }

// Error renders the message in English so a Msg can be used as an error.
func (m Msg) Error() string { return m.In(fallback) }
