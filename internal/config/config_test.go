package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/butthe4d/christmas-draw/internal/locale"
)

const minimal = `
base_url: "https://x.example"
greeting: "Hi {name}! {link}"
people:
  - { name: Ania, phone: "+48600100200" }
  - { name: Bob, phone: "+48600100201" }
  - { name: Cleo, phone: "+48600100202" }
sms:
  token: tok
`

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"600 100 200":      "+48600100200",
		"600-100-200":      "+48600100200",
		"+48 600100200":    "+48600100200",
		"0048600100200":    "+48600100200",
		"+49 1512 3456789": "+4915123456789",
	}
	for in, want := range ok {
		got, err := NormalizePhone(in, "+48")
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"12", "+48abc", "+1234567890123456"} {
		if _, err := NormalizePhone(in, "+48"); err == nil {
			t.Errorf("NormalizePhone(%q) want error", in)
		}
	}
	if _, err := NormalizePhone("600100200", ""); err == nil {
		t.Error("no prefix and no default must fail")
	}
	if got, err := NormalizePhone("+48600100200", ""); err != nil || got != "+48600100200" {
		t.Errorf("got %q, %v", got, err)
	}
}

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaults(t *testing.T) {
	c := mustParse(t, minimal)
	if !c.DryRun || !c.ExcludeCouples || c.MaxResultSegments != 3 || c.Language != "en" ||
		c.SMS.Server != "prod" || c.SMS.PricePerSegment != 0.14 {
		t.Errorf("bad defaults: %+v", c)
	}
	if c.ResultTemplate != "{name}, this year you give a present to: {receiver}. Wishes: {summary} Full list: {link}" ||
		c.ReminderTemplate != "{name}, we are still waiting for your wish list for the gift draw: {link}" ||
		c.EmergencyTemplate != "Change in the gift draw! The previous draw is cancelled, you will get a new one soon." {
		t.Errorf("bad templates: %q %q %q", c.ResultTemplate, c.ReminderTemplate, c.EmergencyTemplate)
	}
}

func TestPolishDefaults(t *testing.T) {
	c := mustParse(t, minimal+"language: pl\n")
	if c.ResultTemplate != "{name}, w tym roku obdarowujesz: {receiver}. Zyczenia: {summary} Cala lista: {link}" ||
		c.ReminderTemplate != "{name}, czekamy na Twoja liste zyczen do losowania prezentow: {link}" ||
		c.EmergencyTemplate != "Zmiana w losowaniu! Poprzednie losowanie jest niewazne, wkrotce dostaniesz nowe." {
		t.Errorf("bad templates: %q %q %q", c.ResultTemplate, c.ReminderTemplate, c.EmergencyTemplate)
	}
}

func TestExplicitFalse(t *testing.T) {
	c := mustParse(t, minimal+"dry_run: false\nexclude_couples: false\n")
	if c.DryRun || c.ExcludeCouples {
		t.Errorf("explicit false ignored: %+v", c)
	}
}

func TestEnvToken(t *testing.T) {
	t.Setenv("CHRISTMAS_DRAW_SMS_TOKEN", "envtok")
	c := mustParse(t, minimal)
	if c.SMS.Token != "envtok" {
		t.Errorf("token = %q", c.SMS.Token)
	}
	// token may come from env only
	c = mustParse(t, strings.Replace(minimal, "  token: tok\n", "  server: prod\n", 1))
	if c.SMS.Token != "envtok" {
		t.Errorf("token = %q", c.SMS.Token)
	}
}

func keys(err error) map[string]int {
	out := map[string]int{}
	var p Problems
	if errors.As(err, &p) {
		for _, m := range p {
			out[m.Key]++
		}
	}
	return out
}

func TestValidateProblems(t *testing.T) {
	t.Setenv("CHRISTMAS_DRAW_SMS_TOKEN", "")
	bad := `
base_url: "ftp://x"
greeting: "Hi 🎄"
language: de
default_country_code: "48"
max_result_segments: 0
people:
  - { name: Ania, phone: "+48600100200" }
  - { name: " Ania ", phone: "+48600100201" }
  - { name: Bob, phone: "+48 600 100 200" }
  - { name: Cleo, phone: "bad" }
couples:
  - [Ania, Zenek]
  - [Bob, Ania]
  - [Ania, Cleo]
exclusions:
  - [Ania, Nobody]
sms:
  server: test
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("want error")
	}
	got := keys(err)
	want := map[string]int{
		"config.name_duplicate": 1, "config.phone_duplicate": 1, "config.phone_invalid": 1,
		"config.couple_unknown": 1, "config.couple_multiple": 1, "config.exclusion_unknown": 1,
		"config.greeting_link": 1, "config.template_non_gsm": 1, "config.base_url": 1,
		"config.sms_server": 1, "config.language": 1, "config.country_code": 1,
		"config.max_segments": 1, "config.sms_token": 1,
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: got %d want %d", k, got[k], n)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected problem %s", k)
		}
	}
	if err.Error() == "" || !strings.Contains(err.Error(), "; ") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestFewPeople(t *testing.T) {
	y := `
base_url: "https://x"
greeting: "{link}"
people:
  - { name: Ania, phone: "+48600100200" }
  - { name: Bob, phone: "+48600100201" }
sms: { token: t }
`
	_, err := Parse([]byte(y))
	if keys(err)["config.people_min"] != 1 || len(err.(Problems)) != 1 {
		t.Errorf("got %v", err)
	}
}

func TestTemplatePlaceholders(t *testing.T) {
	_, err := Parse([]byte(minimal + "result_template: \"hello\"\nreminder_template: \"x\"\n"))
	got := keys(err)
	if got["config.placeholder_missing"] != 3 { // result: receiver+link, reminder: link
		t.Errorf("got %v", got)
	}
	if _, err := Parse([]byte(minimal + "result_template: \"{receiver} {summary} {link}\"\n")); err != nil {
		t.Error(err)
	}
}

func TestOrganiserPhone(t *testing.T) {
	c := mustParse(t, minimal+"default_country_code: \"+48\"\norganiser_phone: \"601 000 000\"\n")
	if c.OrganiserPhone != "+48601000000" {
		t.Errorf("got %q", c.OrganiserPhone)
	}
	_, err := Parse([]byte(minimal + "organiser_phone: \"zz\"\n"))
	if keys(err)["config.organiser_phone"] != 1 {
		t.Errorf("got %v", err)
	}
}

func TestLocalesHaveConfigKeys(t *testing.T) {
	// every Problem key must resolve to real text, not the key itself
	for _, m := range allKeys() {
		if locale.T("en", m, nil) == m || locale.T("pl", m, nil) == m {
			t.Errorf("key %s not translated", m)
		}
	}
}

func allKeys() []string {
	return []string{
		"config.base_url",
		"config.language",
		"config.country_code",
		"config.max_segments",
		"config.sms_server",
		"config.sms_token",
		"config.read_failed",
		"config.yaml_invalid",
		"config.phone_invalid_number",
		"config.organiser_phone",
		"config.placeholder_missing",
		"config.template_non_gsm",
		"config.greeting_link",
		"config.people_min",
		"config.name_empty",
		"config.name_duplicate",
		"config.phone_invalid",
		"config.phone_duplicate",
		"config.couple_unknown",
		"config.couple_self",
		"config.couple_multiple",
		"config.exclusion_unknown",
	}
}

func TestExampleFileLoads(t *testing.T) {
	if _, err := Load("../../config.example.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestMisspelledKeyRejected(t *testing.T) {
	_, err := Parse([]byte(minimal + "exclude_couple: false\n"))
	var m locale.Msg
	if !errors.As(err, &m) || m.Key != "config.yaml_invalid" {
		t.Errorf("got %v", err)
	}
}

func TestErrorsAreMsgs(t *testing.T) {
	var m locale.Msg
	if _, err := NormalizePhone("12", "+48"); !errors.As(err, &m) || m.Key != "config.phone_invalid_number" {
		t.Errorf("got %v", err)
	}
	if _, err := Load("/nonexistent/x.yaml"); !errors.As(err, &m) || m.Key != "config.read_failed" {
		t.Errorf("got %v", err)
	}
	if _, err := Parse([]byte("people: [")); !errors.As(err, &m) || m.Key != "config.yaml_invalid" {
		t.Errorf("got %v", err)
	}
}
