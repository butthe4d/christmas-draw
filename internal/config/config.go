// Package config loads, defaults and validates the YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/smstext"
)

// EnvToken overrides sms.token when set and non-empty.
const EnvToken = "CHRISTMAS_DRAW_SMS_TOKEN"

type Person struct{ Name, Phone string }

type SMS struct {
	Provider, Token, Sender, Server string // Server: "prod" | "sandbox"
	PricePerSegment                 float64
}

type Config struct {
	BaseURL                string
	Greeting               string
	ResultTemplate         string
	ReminderTemplate       string
	EmergencyTemplate      string
	ExcludeCouples         bool
	ListsEditableAfterDraw bool
	DryRun                 bool
	OrganiserPhone         string
	MaxResultSegments      int
	Language               string
	DefaultCountryCode     string
	People                 []Person
	Couples                [][2]string
	Exclusions             [][2]string
	SMS                    SMS
}

type rawPerson struct {
	Name  string `yaml:"name"`
	Phone string `yaml:"phone"`
}

type rawConfig struct {
	BaseURL                string      `yaml:"base_url"`
	Greeting               string      `yaml:"greeting"`
	ResultTemplate         string      `yaml:"result_template"`
	ReminderTemplate       string      `yaml:"reminder_template"`
	EmergencyTemplate      string      `yaml:"emergency_template"`
	ExcludeCouples         *bool       `yaml:"exclude_couples"`
	ListsEditableAfterDraw bool        `yaml:"lists_editable_after_draw"`
	DryRun                 *bool       `yaml:"dry_run"`
	OrganiserPhone         string      `yaml:"organiser_phone"`
	MaxResultSegments      *int        `yaml:"max_result_segments"`
	Language               string      `yaml:"language"`
	DefaultCountryCode     string      `yaml:"default_country_code"`
	People                 []rawPerson `yaml:"people"`
	Couples                [][2]string `yaml:"couples"`
	Exclusions             [][2]string `yaml:"exclusions"`
	SMS                    struct {
		Provider        string   `yaml:"provider"`
		Token           string   `yaml:"token"`
		Sender          string   `yaml:"sender"`
		Server          string   `yaml:"server"`
		PricePerSegment *float64 `yaml:"price_per_segment"`
	} `yaml:"sms"`
}

// Load reads the file at path, applies defaults and the environment, and validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, locale.M("config.read_failed", "path", path, "error", err.Error())
	}
	return Parse(data)
}

// Parse is Load without file IO. A *Config is returned together with a
// Problems error when validation fails, so callers may still inspect it.
func Parse(data []byte) (*Config, error) {
	var r rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled key must not silently fall back to a default
	if err := dec.Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		return nil, locale.M("config.yaml_invalid", "error", err.Error())
	}
	c := &Config{
		BaseURL:                strings.TrimSpace(r.BaseURL),
		Greeting:               strings.TrimSpace(r.Greeting),
		ResultTemplate:         strings.TrimSpace(r.ResultTemplate),
		ReminderTemplate:       strings.TrimSpace(r.ReminderTemplate),
		EmergencyTemplate:      strings.TrimSpace(r.EmergencyTemplate),
		ExcludeCouples:         r.ExcludeCouples == nil || *r.ExcludeCouples,
		ListsEditableAfterDraw: r.ListsEditableAfterDraw,
		DryRun:                 r.DryRun == nil || *r.DryRun,
		MaxResultSegments:      3,
		Language:               strings.TrimSpace(r.Language),
		DefaultCountryCode:     strings.TrimSpace(r.DefaultCountryCode),
		SMS: SMS{
			Provider:        strings.TrimSpace(r.SMS.Provider),
			Token:           strings.TrimSpace(r.SMS.Token),
			Sender:          strings.TrimSpace(r.SMS.Sender),
			Server:          strings.TrimSpace(r.SMS.Server),
			PricePerSegment: 0.14,
		},
	}
	if r.MaxResultSegments != nil {
		c.MaxResultSegments = *r.MaxResultSegments
	}
	if r.SMS.PricePerSegment != nil {
		c.SMS.PricePerSegment = *r.SMS.PricePerSegment
	}
	if c.Language == "" {
		c.Language = "en"
	}
	if c.SMS.Provider == "" {
		c.SMS.Provider = "gsmservice"
	}
	if c.SMS.Server == "" {
		c.SMS.Server = "prod"
	}
	if v := strings.TrimSpace(os.Getenv(EnvToken)); v != "" {
		c.SMS.Token = v
	}
	// Defaults come from the config language; an unknown language falls back
	// to English texts and is reported by Validate.
	for _, d := range []struct {
		dst *string
		key string
	}{
		{&c.ResultTemplate, "sms.result"},
		{&c.ReminderTemplate, "sms.reminder"},
		{&c.EmergencyTemplate, "sms.emergency"},
	} {
		if *d.dst == "" {
			*d.dst = locale.T(c.Language, d.key, nil)
		}
	}
	// Invalid phones are kept (trimmed) so Validate can report them.
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		if n, err := NormalizePhone(s, c.DefaultCountryCode); err == nil {
			return n
		}
		return s
	}
	for _, p := range r.People {
		c.People = append(c.People, Person{Name: strings.TrimSpace(p.Name), Phone: norm(p.Phone)})
	}
	if strings.TrimSpace(r.OrganiserPhone) != "" {
		c.OrganiserPhone = norm(r.OrganiserPhone)
	}
	trimPairs := func(in [][2]string) [][2]string {
		var out [][2]string
		for _, p := range in {
			out = append(out, [2]string{strings.TrimSpace(p[0]), strings.TrimSpace(p[1])})
		}
		return out
	}
	c.Couples = trimPairs(r.Couples)
	c.Exclusions = trimPairs(r.Exclusions)
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

var phoneRE = regexp.MustCompile(`^\+[0-9]{8,15}$`)
var phoneJunk = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "", "\t", "")

// NormalizePhone converts s to E.164; the error is a locale.Msg. Numbers without "+" or "00" get
// defaultCountryCode; with no default they are rejected.
func NormalizePhone(s, defaultCountryCode string) (string, error) {
	n := phoneJunk.Replace(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(n, "+"):
	case strings.HasPrefix(n, "00"):
		n = "+" + n[2:]
	case defaultCountryCode != "":
		n = defaultCountryCode + n
	}
	if !phoneRE.MatchString(n) {
		return "", locale.M("config.phone_invalid_number", "phone", s)
	}
	return n, nil
}

// Problems is a list of validation messages. Error() renders English.
type Problems []locale.Msg

func (p Problems) Error() string {
	parts := make([]string, len(p))
	for i, m := range p {
		parts[i] = m.Error()
	}
	return strings.Join(parts, "; ")
}

// Validate returns Problems, or nil when the config is usable.
func (c *Config) Validate() error {
	var ps Problems
	add := func(key string, kv ...string) { ps = append(ps, locale.M(key, kv...)) }

	if !strings.HasPrefix(c.BaseURL, "http") {
		add("config.base_url")
	}
	if !locale.Has(c.Language) {
		add("config.language", "language", c.Language)
	}
	if c.DefaultCountryCode != "" && !regexp.MustCompile(`^\+[0-9]{1,4}$`).MatchString(c.DefaultCountryCode) {
		add("config.country_code", "value", c.DefaultCountryCode)
	}
	if c.MaxResultSegments < 1 {
		add("config.max_segments")
	}
	if c.SMS.Server != "prod" && c.SMS.Server != "sandbox" {
		add("config.sms_server", "value", c.SMS.Server)
	}
	if c.SMS.Token == "" {
		add("config.sms_token", "env", EnvToken)
	}
	if c.OrganiserPhone != "" {
		if _, err := NormalizePhone(c.OrganiserPhone, c.DefaultCountryCode); err != nil {
			add("config.organiser_phone", "phone", c.OrganiserPhone)
		}
	}

	// templates
	checkTemplate := func(field, text string, required ...string) {
		for _, ph := range required {
			if !strings.Contains(text, ph) {
				add("config.placeholder_missing", "field", field, "placeholder", ph)
			}
		}
		if bad := smstext.NonGSM(smstext.Transliterate(text)); len(bad) > 0 {
			add("config.template_non_gsm", "field", field, "chars", string(bad))
		}
	}
	if c.Greeting == "" || !strings.Contains(c.Greeting, "{link}") {
		add("config.greeting_link")
	}
	checkTemplate("greeting", c.Greeting)
	checkTemplate("result_template", c.ResultTemplate, "{receiver}", "{link}")
	checkTemplate("reminder_template", c.ReminderTemplate, "{link}")
	checkTemplate("emergency_template", c.EmergencyTemplate)

	// people
	if len(c.People) < 3 {
		add("config.people_min", "count", fmt.Sprint(len(c.People)))
	}
	names := map[string]bool{}
	phones := map[string]string{}
	for _, p := range c.People {
		if p.Name == "" {
			add("config.name_empty")
		} else if names[p.Name] {
			add("config.name_duplicate", "name", p.Name)
		}
		names[p.Name] = true
		n, err := NormalizePhone(p.Phone, c.DefaultCountryCode)
		if err != nil {
			add("config.phone_invalid", "name", p.Name, "phone", p.Phone)
			continue
		}
		if other, dup := phones[n]; dup {
			add("config.phone_duplicate", "name", p.Name, "other", other)
		} else {
			phones[n] = p.Name
		}
	}

	// couples and exclusions
	inCouple := map[string]bool{}
	for _, cp := range c.Couples {
		known := true
		for _, n := range cp {
			if !names[n] {
				add("config.couple_unknown", "name", n)
				known = false
			}
		}
		if !known {
			continue
		}
		if cp[0] == cp[1] {
			add("config.couple_self", "name", cp[0])
			continue
		}
		for _, n := range cp {
			if inCouple[n] {
				add("config.couple_multiple", "name", n)
			}
			inCouple[n] = true
		}
	}
	for _, ex := range c.Exclusions {
		for _, n := range ex {
			if !names[n] {
				add("config.exclusion_unknown", "name", n)
			}
		}
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}
