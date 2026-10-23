# Christmas Draw Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A self-hostable Go app that collects family wish lists through private links, runs a sealed Secret Santa draw with exclusion rules, and delivers greetings and results by SMS through gsmservice.

**Architecture:** One Go binary with subcommands. The `round` package owns the lifecycle state machine and every rule. It sits on a SQLite store, a pure draw engine, a pure SMS-text/message layer, and a `Sender` interface (real gsmservice client, or a test fake). The `web` package is a thin server-rendered `html/template` layer over `round`. Dry-run is decided inside `round`: it writes to a log file and changes nothing.

**Tech Stack:** Go ≥ 1.26.8 (required by the SDK's `go.mod`), standard library `net/http` (1.22+ pattern routing), `html/template` + `embed`, `modernc.org/sqlite` (pure Go, so `CGO_ENABLED=0` single binary), `gopkg.in/yaml.v3`, `github.com/gsmservice-pl/messaging-sdk-go/v4`.

**Spec:** `docs/superpowers/specs/2026-10-23-christmas-draw-design.md`. Read it before any task.

## Global Constraints

- Module path: `github.com/butthe4d/christmas-draw`. Binary: `christmas-draw`. Code under `internal/`, entry point `cmd/christmas-draw/main.go`.
- No cgo. `CGO_ENABLED=0 go build ./...` must succeed.
- All randomness (draw, tokens, SMS test code) comes from `crypto/rand`, injected as `io.Reader` where tests need control.
- Tokens: 10 random bytes → base32 (RFC 4648, uppercase, no padding) = 16 characters. Person links: `<base_url>/p/<token>`. Admin link: `<base_url>/admin/<token>`.
- Masked token in dry-run logs and admin previews: `XXXXXXXXXXXXXXXX` (16 × `X`).
- SMS text is always transliterated (ą→a, ć→c, ę→e, ł→l, ń→n, ó→o, ś→s, ź→z, ż→z and capitals) before checks and sending. GSM-7: 160 units single, 153 per segment when joined. Extension characters (`^{}\[~]|€`) count 2 units.
- Segment limits: greeting 2, reminder 2, emergency 2, result `max_result_segments` (default 3), SMS test 1.
- Never write a person token, the admin token (except the one start-up line and `admin-link`), result SMS text, or pairs to normal logs. Don't log request paths.
- The admin page never shows a person's token or result SMS content. Pairs are shown only through Unseal.
- Person identity across config edits = trimmed name (case-sensitive). Phones are compared after normalisation.
- Dry-run never changes state, never records messages, never saves a draw. Its only side effect is appending to the dry-run log. The SMS test is the only send that ignores `dry_run`.
- Default SMS templates (ASCII, gender-neutral, Claude's call):
  - result: `{name}, w tym roku obdarowujesz: {receiver}. Zyczenia: {summary} Cala lista: {link}`
  - reminder: `{name}, czekamy na Twoja liste zyczen do losowania prezentow: {link}`
  - emergency: `Zmiana w losowaniu! Poprzednie losowanie jest niewazne, wkrotce dostaniesz nowe.`
  - test: `christmas-draw test HH:MM kod NNNN` (local time, 4 random digits)
- Claude's calls on spec items left to the builder: Polish numbers only (`+48` + 9 digits). At least 3 people. `dry_run` defaults to `true` when absent, and `exclude_couples` defaults to `true` when absent. The dry-run log shows full text with masked tokens. Unseal records the *first* view time.
- Commit after every task. Messages use the form `feat: …` / `test: …` and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Wish-list titles with emoji or other non-GSM characters** (`🎁`, `ü`, `“”`, `…`). The draw must not be blocked by what a relative typed. Typographic quotes, dashes, and `…` are transliterated (`"`, `-`, `...`). Any other non-GSM rune is dropped from the *SMS summary only*; the web page shows the original. Templates from config still fail validation on non-GSM. Test in Task 4.
2. **A `javascript:` or `data:` URL in an item's shop link.** It's rejected with a form error; only `http://` and `https://` are accepted. Test in Task 10.
3. **Double-click or two tabs pressing "send results" / "send greetings".** Exactly one batch goes out, and the second call gets a "wrong state" error. All `Round` actions hold one mutex, and state is re-read inside it. Test in Task 9.
4. **Editing the config with cosmetic differences** (`"Ania "` vs `"Ania"`, `600 100 200` vs `+48600100200`). The person and their list stay the same; this is not a remove + add. Test in Task 7.
5. **A provider failure in the middle of a batch.** Messages already sent are recorded. Failed people are reported by name, and the state still advances if at least one SMS went out. Pressing the button again (greetings) or "resend" (results) reaches only the people who failed. Test in Task 8.

---

## File Structure

```
cmd/christmas-draw/main.go         subcommands: serve, check, reset, admin-link
internal/config/config.go          YAML load, defaults, env override, validation, phone normalisation
internal/smstext/smstext.go        transliteration, GSM-7 detection, length, segments
internal/draw/draw.go              rules, feasibility (+ explanation), draw, invariant check
internal/message/message.go        template rendering, result summary trimming, links
internal/store/store.go            SQLite schema and queries
internal/sender/sender.go          Sender interface, SMS type, dry-run log
internal/sender/gsmservice.go      gsmservice SDK adapter
internal/sender/fake.go            in-memory fake for tests (exported; used by round and web tests)
internal/round/round.go            Round type, state, Sync (config ↔ DB)
internal/round/collect.go          greetings, reminders, resend, SMS test, previews, statuses
internal/round/drawing.go          draw, results, unseal, change-pending flow
internal/round/person.go           person views and wish-list edits
internal/web/server.go             routes, handlers
internal/web/i18n.go               pl/en strings
internal/web/templates/*.html      person.html, admin.html, unseal.html, layout.html, notfound.html
internal/web/static/style.css
Dockerfile, README.md, config.example.yaml, CLAUDE.md
```

---

### Task 1: Project skeleton and SMS text rules

**Files:**
- Create: `go.mod`, `internal/smstext/smstext.go`, `internal/smstext/smstext_test.go`, `.gitignore` (`/christmas-draw`, `*.db`, `dry-run.log`, `config.yaml`)

**Interfaces:**
- Produces:
  - `func Transliterate(s string) string`
  - `func NonGSM(s string) []rune`: runes not encodable in GSM-7 (basic + extension table), in order, deduplicated
  - `func Length(s string) int`: GSM-7 units (extension chars count 2)
  - `func Segments(s string) int`: `0` for empty, `1` if `Length ≤ 160`, else `ceil(Length/153)`
  - `func StripNonGSM(s string) string`

- [ ] **Step 1: Install Go and init the module.** Install the latest Go 1.26.x (≥ 1.26.8) from go.dev/dl. Run `go mod init github.com/butthe4d/christmas-draw`. Expected: `go version` prints `go1.26.x`.

- [ ] **Step 2: Write failing tests** in `smstext_test.go`:

```go
func TestTransliterate(t *testing.T) {
	cases := map[string]string{
		"Zażółć gęślą jaźń": "Zazolc gesla jazn",
		"ŁĄKA Ść Ń":         "LAKA Sc N",
		"“cytat” – ok…":     `"cytat" - ok...`,
		"„dolny”":           `"dolny"`,
	}
	for in, want := range cases {
		if got := Transliterate(in); got != want {
			t.Errorf("Transliterate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNonGSM(t *testing.T) {
	if got := NonGSM("Hej {name} €5 [ok]"); len(got) != 0 { t.Fatalf("got %q", got) }
	if got := NonGSM("prezent 🎁 ü ü"); !reflect.DeepEqual(got, []rune{'🎁', 'ü'}) { t.Fatalf("got %q", got) }
}

func TestLengthAndSegments(t *testing.T) {
	if Length("a€[") != 5 { t.Fatal("extension chars count 2") }
	if Segments("") != 0 { t.Fatal("empty") }
	if Segments(strings.Repeat("a", 160)) != 1 { t.Fatal("160 = 1") }
	if Segments(strings.Repeat("a", 161)) != 2 { t.Fatal("161 = 2") }
	if Segments(strings.Repeat("a", 306)) != 2 { t.Fatal("306 = 2") }
	if Segments(strings.Repeat("a", 307)) != 3 { t.Fatal("307 = 3") }
}

func TestStripNonGSM(t *testing.T) {
	if got := StripNonGSM("klocki 🎁 lego"); got != "klocki  lego" { t.Fatalf("got %q", got) }
}
```

- [ ] **Step 3: Run** `go test ./internal/smstext/`. Expected: FAIL (undefined functions).

- [ ] **Step 4: Implement** `smstext.go`. Use the GSM 03.38 basic and extension tables as rune sets. Transliteration also maps `“”„«»` → `"`, `‘’‚` → `'`, `–—` → `-`, `…` → `...`.

- [ ] **Step 5: Run** `go test ./internal/smstext/`. Expected: PASS.

- [ ] **Step 6: Commit** `feat: add SMS text rules (transliteration, GSM-7, segments)`.

---

### Task 2: Configuration

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`, `config.example.yaml`

**Interfaces:**
- Consumes: `smstext.Transliterate`, `smstext.NonGSM`
- Produces:

```go
type Person struct{ Name, Phone string }
type SMS struct {
	Provider, Token, Sender, Server string // Server: "prod" | "sandbox"
	PricePerSegment float64               // yaml: price_per_segment, default 0.14
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
	Language               string // "pl" | "en"
	People                 []Person
	Couples                [][2]string
	Exclusions             [][2]string
	SMS                    SMS
}
func Load(path string) (*Config, error) // parse + defaults + env + Validate
func Parse(data []byte) (*Config, error) // same, without file IO (tests)
func NormalizePhone(s string) (string, error)
type Problems []string                  // implements error, joined with "; "
func (c *Config) Validate() error       // returns Problems or nil
```

YAML keys are exactly as in the spec's config example, plus `emergency_template` and `sms.price_per_segment`. Use pointer fields in the YAML struct for `exclude_couples` and `dry_run`, so "absent" defaults to `true`. Env `CHRISTMAS_DRAW_SMS_TOKEN` overrides `sms.token`. Names and phones are trimmed. Phones are normalised in `Parse`.

- [ ] **Step 1: Write failing tests:**
  - `TestNormalizePhone`: `"600 100 200"`, `"600-100-200"`, `"48600100200"`, `"+48 600100200"`, `"0048600100200"` → `"+48600100200"`. `"12345"`, `"+49600100200"`, `"6001002001"` → error.
  - `TestDefaults`: minimal YAML (base_url, greeting with `{link}`, 3 people, sms.token) gives `DryRun==true`, `ExcludeCouples==true`, `MaxResultSegments==3`, `Language=="pl"`, `SMS.Server=="prod"`, `SMS.PricePerSegment==0.14`, and the default result, reminder and emergency templates from Global Constraints.
  - `TestExplicitFalse`: `dry_run: false` and `exclude_couples: false` are respected.
  - `TestEnvToken`: with `t.Setenv("CHRISTMAS_DRAW_SMS_TOKEN","envtok")`, `SMS.Token=="envtok"`.
  - `TestValidateProblems`: one config containing each problem returns `Problems` with one entry per problem. Assert each with `strings.Contains`:
    - duplicate name `"Ania"`
    - duplicate phone
    - couple referring to unknown `"Zenek"`
    - `"Ania"` in two couples
    - exclusion with unknown name
    - only 2 people (`"at least 3"`)
    - greeting without `{link}`
    - greeting containing `🎄` (non-GSM after transliteration)
    - `base_url` not starting with `http`
    - `sms.server` `"test"`
    - `language` `"de"`
    - `max_result_segments: 0`
  - `TestTemplatePlaceholders`: result template without `{link}` or `{receiver}` is a problem.

- [ ] **Step 2: Run** `go test ./internal/config/`. Expected: FAIL.

- [ ] **Step 3: Implement** `config.go` and write `config.example.yaml` (the spec's example, with comments, `dry_run: true`).

- [ ] **Step 4: Run** `go test ./internal/config/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add config loading and validation`.

---

### Task 3: Draw engine

**Files:**
- Create: `internal/draw/draw.go`, `internal/draw/draw_test.go`

**Interfaces:**
- Produces:

```go
type Rules struct { /* unexported */ }
func NewRules(names []string, couples, exclusions [][2]string, excludeCouples bool) Rules
func (r Rules) Names() []string
func (r Rules) Allowed(giver, receiver string) bool  // false for self and forbidden pairs, both directions
type Assignment map[string]string                    // giver → receiver
type InfeasibleError struct {
	Givers    []string // a set of givers that together...
	Receivers []string // ...can only draw from these (fewer) receivers
}
func (e *InfeasibleError) Error() string
func Feasible(r Rules) error                   // nil or *InfeasibleError
func Draw(r Rules, rnd io.Reader) (Assignment, error)
func Check(r Rules, a Assignment) error        // describes the first violated invariant
```

Feasibility algorithm: bipartite matching (Kuhn's augmenting paths) between givers and receivers over `Allowed`. If some giver `g` stays unmatched, run one more alternating-path search from `g`. `Givers` = givers reached, `Receivers` = receivers reached (Hall violator, `len(Receivers) < len(Givers)`). Sort both for determinism. `Error()`: `"<Givers joined ", "> can only draw from <Receivers joined ", "> — not enough people"`. If `Receivers` is empty: `"<giver> has nobody they are allowed to draw"`.

Draw algorithm: return `Feasible`'s error if infeasible. Otherwise do rejection sampling: Fisher–Yates shuffle with `rnd`, accept if every pair is allowed. Use up to 200 000 attempts, then fall back to randomized backtracking (candidates shuffled with `rnd`). Always run `Check` before returning.

- [ ] **Step 1: Write failing tests:**
  - `TestAllowed`: self false; couple both directions false when `excludeCouples`, true when not; exclusion false both directions even with `excludeCouples=false`.
  - `TestFeasibleExplains`: 3 people `A,B,C`, couple `A–B` → `*InfeasibleError` with `Givers=[A B]`, `Receivers=[C]`. 4 people as 2 couples → `nil`. `A` excluded with everyone → `Receivers` empty, message contains `"A has nobody"`.
  - `TestDrawInvariantsProperty`: for 2000 random setups (`math/rand/v2` seeded for reproducibility; 3–15 names, random couples and exclusions), if `Feasible` is nil then `Draw` (with `crypto/rand.Reader`) returns an assignment that passes `Check`. If not nil, `Draw` returns an `*InfeasibleError`. Cross-check feasibility against brute-force permutation search for n ≤ 7.
  - `TestDrawUniformSmall`: `A,B,C`, no rules → 2 valid assignments, 6000 draws, each between 2700 and 3300.
  - `TestDrawBarelyFeasible`: 6 people where each giver has exactly one allowed receiver forming a single cycle → `Draw` returns exactly that cycle, within 1s.
  - `TestCheck`: rejects self-draw, forbidden pair, someone receiving twice, someone missing, unknown name.

- [ ] **Step 2: Run** `go test ./internal/draw/`. Expected: FAIL.

- [ ] **Step 3: Implement** `draw.go`.

- [ ] **Step 4: Run** `go test ./internal/draw/ -count=1`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add draw engine with feasibility explanation`.

---

### Task 4: Message composition

**Files:**
- Create: `internal/message/message.go`, `internal/message/message_test.go`

**Interfaces:**
- Consumes: `smstext.*`
- Produces:

```go
const MaskedToken = "XXXXXXXXXXXXXXXX"
func Link(baseURL, token string) string   // trims trailing "/" from baseURL, then "/p/"+token
type TooLongError struct{ Segments, Max int }
type NotGSMError struct{ Runes []rune }
func Render(tmpl string, vars map[string]string, maxSegments int) (string, error)
func Result(tmpl, name, receiver, link string, titles []string, maxSegments int) (string, error)
```

`Render` replaces `{key}` for every key in `vars`, then transliterates, then returns `*NotGSMError` / `*TooLongError` if it doesn't fit. Names and links are inserted as given, then transliterated with the rest.

`Result` builds `{summary}` from titles: each title is transliterated, then `StripNonGSM`, then trimmed, and empty ones are skipped. Titles are joined with `", "`. Take the longest prefix of titles whose rendered SMS fits `maxSegments`. If not all fit, append `"..."` (still within limit). If no title fits whole, cut the first title rune-by-rune and add `"..."`. If even `summary="..."` doesn't fit, return `*TooLongError`.

- [ ] **Step 1: Write failing tests:**
  - `TestLink`: `Link("https://x.ts.net/", "AB")` == `"https://x.ts.net/p/AB"`.
  - `TestRender`: `Render("Hej {name}! {link}", {"name":"Łucja","link":"L"}, 1)` == `"Hej Lucja! L"`. Template with `🎄` → `*NotGSMError`. A 200-char result with max 1 → `*TooLongError{Segments:2, Max:1}`.
  - `TestResultFitsAll`: 3 short titles, max 3 → summary `"a, b, c"` with no `"..."`.
  - `TestResultTrims`: 30 titles of 20 chars, max 2 → `smstext.Segments(out) <= 2`, ends with `"... Cala lista: <link>"` (with the default template), and contains the first title.
  - `TestResultEmojiTitle`: titles `["🎁", "klocki 🎁 lego"]` → summary is exactly `"klocki  lego"` (first title empty after stripping, so skipped), no error. *(Review Focus 1)*
  - `TestResultHugeSingleTitle`: one 1000-char title, max 1 → fits in 1 segment and contains `"..."`.
  - `TestResultImpossible`: receiver name of 400 chars, max 1 → `*TooLongError`.

- [ ] **Step 2: Run** `go test ./internal/message/`. Expected: FAIL.

- [ ] **Step 3: Implement** `message.go`.

- [ ] **Step 4: Run** `go test ./internal/message/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add SMS message composition and summary trimming`.

---

### Task 5: SQLite store

**Files:**
- Create: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces:**
- Produces:

```go
var ErrNotFound = errors.New("not found")
type Person struct{ ID int64; Name, Phone, Token string; Greeted bool }
type Item struct{ ID, PersonID int64; Position int; Title, Link, Note string }
type Message struct {
	ID, PersonID int64   // PersonID 0 = none (SMS test, or person since deleted)
	Kind       string    // "greeting" | "reminder" | "result" | "emergency" | "test"
	ProviderID int64
	Status     string
	Segments   int
	SentAt     time.Time
}
func Open(path string) (*Store, error)                 // creates schema; PRAGMA foreign_keys=ON, journal_mode=WAL
func (s *Store) Close() error
func (s *Store) Tx(ctx context.Context, fn func(*Store) error) error
func (s *Store) Meta(ctx, key string) (string, bool, error)
func (s *Store) SetMeta(ctx, key, value string) error
func (s *Store) People(ctx) ([]Person, error)           // ordered by ID
func (s *Store) PersonByToken(ctx, token string) (Person, error)
func (s *Store) AddPerson(ctx, name, phone, token string) (Person, error)
func (s *Store) UpdatePhone(ctx, id int64, phone string) error
func (s *Store) SetGreeted(ctx, id int64, greeted bool) error
func (s *Store) DeletePerson(ctx, id int64) error       // cascades items and pairs; messages.person_id → NULL
func (s *Store) Items(ctx, personID int64) ([]Item, error) // ordered by Position, ID
func (s *Store) ItemCounts(ctx) (map[int64]int, error)
func (s *Store) AddItem(ctx, it Item) (Item, error)     // Position = max+1 for that person
func (s *Store) UpdateItem(ctx, it Item) error          // WHERE id AND person_id; ErrNotFound if 0 rows
func (s *Store) DeleteItem(ctx, personID, itemID int64) error // same scoping
func (s *Store) SavePairs(ctx, pairs map[int64]int64) error   // replaces all
func (s *Store) Pairs(ctx) (map[int64]int64, error)
func (s *Store) ClearPairs(ctx) error
func (s *Store) RecordMessage(ctx, m Message) (Message, error)
func (s *Store) Messages(ctx) ([]Message, error)        // newest first
func (s *Store) UpdateMessageStatus(ctx, id int64, status string) error
func (s *Store) Reset(ctx) error                        // deletes all rows from all tables
```

Tables: `people(id, name UNIQUE, phone, token UNIQUE, greeted)`, `items(id, person_id FK ON DELETE CASCADE, position, title, link, note)`, `pairs(giver_id PK FK CASCADE, receiver_id FK CASCADE)`, `messages(id, person_id FK ON DELETE SET NULL, kind, provider_id, status, segments, sent_at)`, `meta(key PK, value)`. Message text is never stored.

- [ ] **Step 1: Write failing tests** (each on `Open(filepath.Join(t.TempDir(),"t.db"))`):
  - `TestPeopleCRUD`: add 2, `PersonByToken` finds one, unknown token → `ErrNotFound`, duplicate name → error.
  - `TestItemsScopedToPerson`: `UpdateItem` / `DeleteItem` with another person's ID → `ErrNotFound`, item unchanged. *(security)*
  - `TestDeleteCascades`: delete a person with items, a pair, and a message → items and pairs gone, message kept with `PersonID==0`.
  - `TestTxRollback`: `Tx` whose fn adds a person then returns an error → person absent.
  - `TestMetaAndReset`: set/get meta. `Reset` empties every table, including meta.
  - `TestReopen`: data survives `Close` + `Open`.

- [ ] **Step 2: Run** `go test ./internal/store/`. Expected: FAIL.

- [ ] **Step 3: Implement** `store.go` (`go get modernc.org/sqlite`). The `Store` wraps a small interface satisfied by both `*sql.DB` and `*sql.Tx`, so `Tx` hands back a `*Store` bound to the transaction.

- [ ] **Step 4: Run** `go test ./internal/store/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add SQLite store`.

---

### Task 6: SMS senders

**Files:**
- Create: `internal/sender/sender.go`, `internal/sender/gsmservice.go`, `internal/sender/fake.go`, `internal/sender/sender_test.go`, `internal/sender/gsmservice_integration_test.go`

**Interfaces:**
- Produces:

```go
type SMS struct{ To, Text string }
type Result struct{ ProviderID int64; Status string }
type Sender interface {
	Send(ctx context.Context, m SMS) (Result, error)           // error if the gateway rejects it
	Price(ctx context.Context, ms []SMS) (float64, error)      // total PLN
	Status(ctx context.Context, ids []int64) (map[int64]string, error)
}
func NewGSMService(token, senderName, server string) Sender
type DryRunLog struct{ Path string }
func (l DryRunLog) Write(kind string, ms []SMS, now time.Time) error // appends a block
type Fake struct {                     // safe for concurrent use
	Sent      []SMS
	FailTo    map[string]bool           // Send to these numbers returns an error
	PriceErr  error
	Statuses  map[int64]string
}
func (f *Fake) Send(...); func (f *Fake) Price(...); func (f *Fake) Status(...)
```

`GSMService`: `v4.New(v4.WithSecurity(token), v4.WithServer(server))`. `Send` uses `operations.CreateSendSmsRequestBodySmsMessage` with `Unicode: false` and `Sender` only if non-empty. It returns `Result{ProviderID: *msg.ID, Status: *msg.StatusCode}`. Look up which `StatusCode` values mean "rejected" in the REST docs (https://api.szybkisms.pl/rest/) and return an error with `StatusDescription` for those. `Price` sends up to 100 messages per `GetPrice` call and sums `Price`; any `Price.Error` is an error. `Status` uses `Outgoing.GetByIds`. `Fake.Price` returns `PriceErr` or `0.14 × total segments`. `Fake` IDs count up from 1.

Dry-run block format (one per call):
```
=== 2026-12-01 20:15:03 greeting (3 SMS) ===
+48600100200 [1 seg] Hej Ania! ... https://x/p/XXXXXXXXXXXXXXXX
```

- [ ] **Step 1: Write failing tests:**
  - `TestDryRunLogAppends`: two writes → file contains both headers in order, each line has phone, `[N seg]`, and text.
  - `TestFakeFailTo`: a `FailTo` number gives an error and isn't added to `Sent`. Another number succeeds with IDs 1, 2.
  - `TestGSMServiceSandbox` (integration file, `//go:build integration`): skip unless `GSMSERVICE_SANDBOX_TOKEN` is set. Send one SMS on `sandbox` and assert `ProviderID > 0`, then `Status` returns that ID.

- [ ] **Step 2: Run** `go test ./internal/sender/`. Expected: FAIL.

- [ ] **Step 3: Implement** the three files (`go get github.com/gsmservice-pl/messaging-sdk-go/v4`).

- [ ] **Step 4: Run** `go test ./internal/sender/` → PASS, and `go vet -tags integration ./internal/sender/` → no errors.

- [ ] **Step 5: Commit** `feat: add gsmservice sender, dry-run log and fake`.

---

### Task 7: Round: state and config sync

**Files:**
- Create: `internal/round/round.go`, `internal/round/round_test.go`, `internal/round/helpers_test.go`

**Interfaces:**
- Consumes: `config.Config`, `store.*`, `draw.NewRules`, `sender.Sender`, `sender.DryRunLog`
- Produces:

```go
type State string
const (Setup State = "SETUP"; Collecting = "COLLECTING"; Drawn = "DRAWN"; Delivered = "DELIVERED"; ChangePending = "CHANGE_PENDING")
var ErrWrongState = errors.New("action not allowed in current state")
type Options struct{ Now func() time.Time; Rand io.Reader } // zero → time.Now, crypto/rand.Reader
type Round struct{ /* cfg, st, snd, dry, opts, mu sync.Mutex */ }
func New(cfg *config.Config, st *store.Store, snd sender.Sender, dry sender.DryRunLog, o Options) *Round
type Diff struct{ Added, Removed []string; PhoneChanged []string; RulesChanged bool }
func (d Diff) Structural() bool // len(Added)+len(Removed)>0 || RulesChanged
type SyncReport struct{ Diff Diff; NewState State; AdminTokenCreated bool }
func (r *Round) Sync(ctx context.Context) (SyncReport, error)
func (r *Round) State(ctx context.Context) (State, error)
func (r *Round) AdminToken(ctx context.Context) (string, error)
func (r *Round) Rules() draw.Rules
func newToken(rnd io.Reader) (string, error)
```

Meta keys: `state`, `admin_token`, `rules_hash` (SHA-256 of sorted, normalised couples + exclusions + `exclude_couples`), `unsealed_at` (RFC 3339).

`Sync` behaviour, in one transaction:
- Empty DB: create admin token, add all people with new tokens, state `SETUP`, store `rules_hash`.
- Phone changes are always applied.
- `Setup`/`Collecting`: apply adds (new token, `Greeted=false`) and removes. Update `rules_hash`.
- `Drawn` with a structural diff: apply, `ClearPairs`, state `Collecting`.
- `Delivered` with a structural diff: apply *nothing structural*, state `ChangePending`.
- `ChangePending`: structural diff empty → state `Delivered`. Otherwise stay pending.

- [ ] **Step 1: Write the test helper** `newTestRound(t, yaml string) (*Round, *store.Store, *sender.Fake, string /*dryLogPath*/)` in `helpers_test.go`. It uses a temp DB, `config.Parse`, a fixed `Now`, and `crypto/rand`. Add `setState(t, st, State)`.

- [ ] **Step 2: Write failing tests:**
  - `TestSyncFresh`: 3 people → 3 tokens, all 16 chars `[A-Z2-7]`, distinct. Admin token set and different from person tokens. State `SETUP`. `AdminTokenCreated==true`, and false on a second `Sync`.
  - `TestSyncCosmeticEdits` *(Review Focus 4)*: add an item for Ania, re-sync with `name: "Ania "` and the phone written as `600 100 200` → same person ID and token, item still there, `Diff` empty.
  - `TestSyncAddRemoveCollecting`: in `Collecting`, add Zenek and remove Tomek → Zenek has a token and `Greeted=false`, Tomek's token gives `ErrNotFound`, other items intact.
  - `TestSyncDrawnStructural`: `Drawn` with pairs, add a person → pairs cleared, state `Collecting`.
  - `TestSyncDeliveredPending`: `Delivered`, remove Tomek → state `ChangePending`, Tomek still in DB, pairs intact. Revert config and `Sync` → `Delivered`.
  - `TestSyncPhoneOnlyDelivered`: phone change in `Delivered` → applied, state unchanged.
  - `TestSyncRulesChangeDelivered`: new exclusion in `Delivered` → `ChangePending`.

- [ ] **Step 3: Run** `go test ./internal/round/`. Expected: FAIL.

- [ ] **Step 4: Implement** `round.go`.

- [ ] **Step 5: Run** `go test ./internal/round/`. Expected: PASS.

- [ ] **Step 6: Commit** `feat: add round state machine and config sync`.

---

### Task 8: Round: greetings, reminders, SMS test, previews, statuses

**Files:**
- Create: `internal/round/collect.go`, `internal/round/collect_test.go`

**Interfaces:**
- Consumes: Task 7, `message.Render`, `message.Link`, `message.MaskedToken`
- Produces:

```go
type Preview struct {
	Messages, Segments int
	Cost               float64
	CostEstimated      bool     // true when Sender.Price failed and config price was used
	Sample             string   // first message with masked token; "" for results
	Problems           []string // blocking problems (template too long, not GSM, infeasible, ...)
}
type Outcome struct {
	DryRun   bool
	Sent     int
	Failed   []string // person names
	LogPath  string   // set when DryRun
}
type TestResult struct{ To, Text string; ProviderID int64; Status string }
type PersonStatus struct{ ID int64; Name string; Items int; Greeted bool; LastKind, LastStatus string }
type Overview struct {
	State       State
	People      []PersonStatus
	Ungreeted   int
	Infeasible  error         // draw.Feasible result
	UnsealedAt  *time.Time
	Pending     *Diff         // non-nil in ChangePending; names of added/removed are shown
	DryRun      bool
}
func (r *Round) Overview(ctx context.Context) (Overview, error)
func (r *Round) PreviewGreetings(ctx context.Context) (Preview, error)
func (r *Round) SendGreetings(ctx context.Context) (Outcome, error)  // Setup|Collecting; to ungreeted only
func (r *Round) PreviewReminders(ctx context.Context) (Preview, error)
func (r *Round) SendReminders(ctx context.Context) (Outcome, error)  // Collecting; people with 0 items
func (r *Round) ResendGreeting(ctx context.Context, personID int64) (Outcome, error) // not Setup
func (r *Round) SendTestSMS(ctx context.Context) (TestResult, error) // any state, ignores DryRun; needs OrganiserPhone
func (r *Round) RefreshStatuses(ctx context.Context) error           // messages ≤72h old with ProviderID, max once per 30s
```

A shared private helper `send(ctx, kind string, batch []outgoing) (Outcome, error)` does the work for every send in Tasks 8–9. `outgoing` is `{personID int64; sms sender.SMS; masked string}`.
- In dry-run, write the `masked` texts to the log and return.
- Otherwise send each message, record a `store.Message` for each success, and collect failures by name. Return an error only if *all* fail.

Callers change state only when `Outcome.Sent > 0` and not dry-run. Every exported action takes `r.mu`.

- [ ] **Step 1: Write failing tests:**
  - `TestSendGreetingsAdvances`: `dry_run: false` → 3 SMS to the 3 phones. Each text contains that person's own `/p/<token>` and has no `XXXX`. State `Collecting`, all `Greeted`, 3 `greeting` messages recorded.
  - `TestSendGreetingsDryRun`: `dry_run: true` → `Fake.Sent` empty, log contains 3 lines with `/p/XXXXXXXXXXXXXXXX` and no real token, state still `Setup`, no messages recorded.
  - `TestGreetingsOnlyUngreeted`: after sending, add Zenek, `Sync`, send again → exactly 1 SMS, to Zenek.
  - `TestPartialFailure` *(Review Focus 5)*: `FailTo` Tomek → `Outcome.Sent==2`, `Failed==["Tomek"]`, state `Collecting`, Tomek still ungreeted. Clear `FailTo` and send again → exactly 1 SMS, to Tomek.
  - `TestAllFail`: every number fails → error, state `Setup`.
  - `TestReminders`: Ania has an item → reminders go only to the other two. In `Setup` → `ErrWrongState`.
  - `TestPreviewGreetings`: `Messages==3`, `Sample` contains `XXXXXXXXXXXXXXXX`, `Cost==0.14*Segments`. With `PriceErr` set, the same cost and `CostEstimated==true`. A greeting too long for 2 segments → `Problems` non-empty, and `SendGreetings` returns an error without sending.
  - `TestSendTestSMS`: with `dry_run: true`, 1 SMS to `organiser_phone`, text matches `^christmas-draw test \d\d:\d\d kod \d{4}$`, recorded as kind `test`. With no `organiser_phone` → error.
  - `TestRefreshStatuses`: `Fake.Statuses[1]="DELIVERED"` → after refresh, message 1 has that status. A second call within 30s doesn't call `Status` again.

- [ ] **Step 2: Run** `go test ./internal/round/ -run 'Greet|Remind|Preview|TestSMS|Refresh|Partial|AllFail'`. Expected: FAIL.

- [ ] **Step 3: Implement** `collect.go`.

- [ ] **Step 4: Run** `go test ./internal/round/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add greetings, reminders, SMS test and previews`.

---

### Task 9: Round: draw, results, unseal, change-pending flow

**Files:**
- Create: `internal/round/drawing.go`, `internal/round/drawing_test.go`

**Interfaces:**
- Consumes: Tasks 7–8, `draw.*`, `message.Result`
- Produces:

```go
type DrawOutcome struct{ DryRun bool; Problems []string; LogPath string }
type Pair struct{ Giver, Receiver string }
func (r *Round) Draw(ctx context.Context) (DrawOutcome, error)        // Collecting|Drawn (redo)
func (r *Round) PreviewResults(ctx context.Context) (Preview, error)  // Drawn; Sample always ""
func (r *Round) SendResults(ctx context.Context) (Outcome, error)     // Drawn → Delivered
func (r *Round) ResendResult(ctx context.Context, personID int64) (Outcome, error) // Delivered|ChangePending
func (r *Round) Unseal(ctx context.Context) ([]Pair, error)           // Drawn|Delivered|ChangePending
func (r *Round) PreviewEmergency(ctx context.Context, text string) (Preview, error) // ChangePending
func (r *Round) ConfirmChange(ctx context.Context, text string) (Outcome, error)    // ChangePending → Collecting
```

`Draw` problems (all collected, not just the first):
- someone has 0 items (`"<name>: empty wish list"`)
- `Feasible` error
- any result SMS that `message.Result` can't fit

If there are problems, return them with no state change. On success:
- **Dry-run:** write the result SMS (masked links) to the log. Pairs aren't saved, and state doesn't change.
- **Otherwise:** `Check`, then `SavePairs` and state `Drawn` in one `Tx`.

Each result SMS is `message.Result(cfg.ResultTemplate, giver, receiver, Link(base, giverToken), receiverTitles, cfg.MaxResultSegments)`. The link is the *giver's* page.

`ConfirmChange`, in one `Tx` before sending:
1. Render the emergency `text` (`{name}`, `{link}`, max 2 segments). If it fails, return an error.
2. Recompute the diff and apply it (adds and removes), `ClearPairs`, update `rules_hash`, state `Collecting`.
3. After the commit, send: emergency to everyone in the *new* group except newcomers, then a greeting to newcomers.

In dry-run, steps 2–3 become log-only: no DB changes, state stays `ChangePending`.

`Unseal` sets `unsealed_at` only if it's unset.

- [ ] **Step 1: Write failing tests:**
  - `TestDrawBlockedEmptyList`: one person without items → `Problems` contains `"Tomek: empty wish list"`, state `Collecting`, no pairs.
  - `TestDrawInfeasible`: 3 people, couple → `Problems` contains the `InfeasibleError` text.
  - `TestDrawSaves`: all have items → state `Drawn`, `Pairs` passes `draw.Check`.
  - `TestDrawDryRun`: `dry_run: true` → no pairs, state `Collecting`, log has 3 result lines with masked links.
  - `TestRedraw`: `Draw` in `Drawn` works. In `Delivered` → `ErrWrongState`.
  - `TestSendResults`: each SMS goes to the giver's phone, contains the receiver's name and first item title, and links to the giver's own token. State `Delivered`. `PreviewResults` beforehand had `Sample==""` and `Messages==3`.
  - `TestConcurrentSendResults` *(Review Focus 3)*: two goroutines call `SendResults` → `len(Fake.Sent)==3` in total, and one call returns `ErrWrongState`.
  - `TestUnseal`: returns 3 pairs matching `Pairs`. `Overview.UnsealedAt` is set, and a second unseal keeps the first time (advance `Now`). `Setup` → `ErrWrongState`.
  - `TestConfirmChangeRemove`: `Delivered`, remove Tomek, `Sync` → `ChangePending`. `ConfirmChange("Zmiana! {link}")` → emergency SMS to exactly the 2 remaining phones, nothing to Tomek. Tomek's token → `ErrNotFound`. Pairs empty, state `Collecting`, remaining items intact.
  - `TestConfirmChangeAdd`: add Zenek → 3 emergency SMS (old members) and 1 greeting to Zenek. Then `Draw` is blocked until Zenek has an item.
  - `TestConfirmChangeDryRun`: nothing changes, log contains the emergency lines.
  - `TestConfirmChangeTooLong`: 400-char text → error, still `ChangePending`, nothing sent.

- [ ] **Step 2: Run** `go test ./internal/round/ -run 'Draw|Results|Unseal|Change'`. Expected: FAIL.

- [ ] **Step 3: Implement** `drawing.go`.

- [ ] **Step 4: Run** `go test ./internal/round/ -race`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add draw, results, unseal and setup-changed flow`.

---

### Task 10: Person pages

**Files:**
- Create: `internal/round/person.go`, `internal/round/person_test.go`, `internal/web/server.go`, `internal/web/i18n.go`, `internal/web/templates/layout.html`, `internal/web/templates/person.html`, `internal/web/templates/notfound.html`, `internal/web/static/style.css`, `internal/web/person_test.go`

**Interfaces:**
- Consumes: Tasks 7–9
- Produces:

```go
// round
type ItemInput struct{ Title, Link, Note string }
var ErrInvalidItem = errors.New("invalid item") // wrapped with a reason
var ErrNotEditable = errors.New("wish list is frozen")
type PersonView struct {
	Name     string
	Items    []store.Item
	Editable bool
	Receiver *ReceiverView // non-nil only in Delivered|ChangePending
}
type ReceiverView struct{ Name string; Items []store.Item }
func (r *Round) PersonView(ctx context.Context, token string) (PersonView, error) // store.ErrNotFound for unknown
func (r *Round) AddItem(ctx context.Context, token string, in ItemInput) error
func (r *Round) UpdateItem(ctx context.Context, token string, id int64, in ItemInput) error
func (r *Round) DeleteItem(ctx context.Context, token string, id int64) error
// web
func NewServer(r *round.Round, cfg *config.Config) http.Handler
func T(lang, key string) string // i18n lookup, falls back to "pl"
```

`Editable` = state is `Setup` or `Collecting`, or `cfg.ListsEditableAfterDraw`.

Item validation:
- title is required after trimming, ≤ 200 runes
- note ≤ 500 runes
- link is empty or parses with scheme `http`/`https` and a host, ≤ 500 runes

Routes:
- `GET /p/{token}`
- `POST /p/{token}/items`
- `POST /p/{token}/items/{id}`
- `POST /p/{token}/items/{id}/delete`
- `GET /static/`

POSTs redirect 303 to `/p/{token}`. Errors re-render the page with a translated message. Unknown tokens get a 404 `notfound.html`, the same page for every unknown token. Language: `?lang=pl|en` sets cookie `lang` (1 year); otherwise cookie, then `cfg.Language`.

The page layout:
- Your own list with add / edit / delete forms when `Editable`.
- When `Receiver` is set, a "letter to Santa" card at the top: the receiver's name, and each item with its title, a link (`rel="noopener noreferrer"`, `target="_blank"`), and the note.
- Festive CSS, mobile-first, no JavaScript.

- [ ] **Step 1: Write failing round tests** (`person_test.go`):
  - `TestEditableByState`: editable in `Setup` and `Collecting`; `ErrNotEditable` in `Drawn` and `Delivered`; editable in `Delivered` with `lists_editable_after_draw: true`.
  - `TestReceiverOnlyAfterDelivery`: `Receiver==nil` in `Drawn`. In `Delivered`, it's the paired receiver with their items.
  - `TestItemValidation`: empty title, 201-rune title, link `javascript:alert(1)`, link `data:text/html,x`, link `ftp://x` → `ErrInvalidItem`. `https://allegro.pl/x` OK. *(Review Focus 2)*
  - `TestEditOtherPersonsItem`: `UpdateItem` with my token and Ania's item ID → `store.ErrNotFound`.

- [ ] **Step 2: Write failing web tests** (`internal/web/person_test.go`, `httptest`):
  - `TestPersonPageRenders`: `GET /p/<token>` → 200, body contains the name.
  - `TestUnknownToken`: random token → 404. The admin token used as `/p/<admin>` → 404.
  - `TestAddItemFlow`: POST a form with a title → 303, then the GET shows the title.
  - `TestJavascriptLinkRejected`: POST with link `javascript:alert(1)` → 200 with an error message, and the item isn't stored.
  - `TestTitleEscaped`: title `<script>x</script>` is shown escaped.
  - `TestLangSwitch`: `?lang=en` → `Set-Cookie: lang=en` and an English string from `i18n.go`.

- [ ] **Step 3: Run** `go test ./internal/round/ ./internal/web/`. Expected: FAIL.

- [ ] **Step 4: Implement** `person.go`, `server.go` (person routes), `i18n.go`, the templates, and the CSS. Templates and static files use `//go:embed`.

- [ ] **Step 5: Run** `go test ./internal/round/ ./internal/web/`. Expected: PASS.

- [ ] **Step 6: Commit** `feat: add person wish-list pages`.

---

### Task 11: Admin page

**Files:**
- Modify: `internal/web/server.go`, `internal/web/i18n.go`
- Create: `internal/web/templates/admin.html`, `internal/web/templates/unseal.html`, `internal/web/admin_test.go`

**Interfaces:**
- Consumes: every exported `Round` method from Tasks 7–9.

Routes. All start with `/admin/{token}`. An invalid admin token gets the same 404 page as the person routes.

| Method, path | Calls |
|---|---|
| `GET ` | `RefreshStatuses`, `Overview`, plus previews valid for the state |
| `POST /greetings` | `SendGreetings` |
| `POST /reminders` | `SendReminders` |
| `POST /people/{id}/greeting` | `ResendGreeting` |
| `POST /test-sms` | `SendTestSMS` |
| `POST /draw` | `Draw` |
| `POST /results` | `SendResults` |
| `POST /people/{id}/result` | `ResendResult` |
| `POST /unseal` | `Unseal`; renders `unseal.html` directly (no redirect) |
| `POST /change` | form `text`, `action=preview|send` → `PreviewEmergency` / `ConfirmChange` |

What the page shows:
- **Status:** the state, a dry-run banner, and the config path to the dry-run log when relevant.
- **People table:** name, number of items, greeted yes/no, last message kind and status. No tokens, ever.
- **Checks:** feasibility in plain words (from `InfeasibleError`).
- **Action:** the next action for the state, with its preview (messages, segments, cost, "estimated" marker, problems).
- **Unsealed notice:** `"results were viewed on <date> at <time>"` when unsealed.
- **Change pending:** added and removed names, a textarea prefilled with `cfg.EmergencyTemplate`, and preview and send buttons.

Every POST that sends SMS or unseals needs a `confirm=yes` form field, which comes from a checkbox in the form. Without it the page re-renders with "please confirm". After an action, redirect 303 to the admin page with a flash message in the query: `?msg=<i18n key>&n=<count>`. Failed names are shown too.

- [ ] **Step 1: Write failing tests** (`admin_test.go`):
  - `TestAdminNoTokensLeak`: in every state (`Setup` → `Collecting` → `Drawn` → `Delivered` → `ChangePending`), the admin body contains no person token, and (from `Drawn` on) none of the result SMS texts that `Fake.Sent` received or that `message.Result` would produce.
  - `TestAdminConfirmRequired`: `POST /greetings` without `confirm` → nothing sent.
  - `TestAdminFlow`: greetings → add items through the person routes → draw → results, each through HTTP. Ends `Delivered` with 3 result SMS in the Fake.
  - `TestAdminUnsealPage`: shows 3 pairs. Afterwards the admin GET shows the unsealed notice.
  - `TestAdminChangeFlow`: remove a person in config and `Sync` → admin shows their name under "removed" and the prefilled textarea. Preview then send → `Collecting`.
  - `TestAdminBadToken`: `GET /admin/<person token>` → 404.

- [ ] **Step 2: Run** `go test ./internal/web/`. Expected: FAIL.

- [ ] **Step 3: Implement** the admin handlers and templates.

- [ ] **Step 4: Run** `go test ./internal/web/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat: add admin page`.

---

### Task 12: CLI, Docker, docs

**Files:**
- Create: `cmd/christmas-draw/main.go`, `cmd/christmas-draw/main_test.go`, `Dockerfile`, `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: everything.
- Produces: `func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int` (testable `main`).

Flags (shared by all commands):
- `-config` (default `config.yaml`)
- `-db` (default `christmas-draw.db`)
- `-addr` (default `:8080`, `serve` only)

The dry-run log is `dry-run.log` next to the DB.

Commands:
- `serve` (also the default when no command is given): `Load`, `Open`, `Sync`. On first start, print `Admin page: <base_url>/admin/<token>` once. Then `http.ListenAndServe` with graceful shutdown on SIGINT/SIGTERM.
- `check`: `Load`, validate, `draw.Feasible`, render the greeting for the first person. Print problems and exit 1, or print `OK` and exit 0. Doesn't touch the DB.
- `reset`: print what will be wiped and ask the user to type `reset`; `-yes` skips the prompt. Then `Store.Reset`.
- `admin-link`: print the admin URL from the DB, or exit 1 if there isn't one yet.

Logging: `log/slog` text handler, with no paths or tokens.

Dockerfile: `golang:1.26` build stage with `CGO_ENABLED=0 go build -o /christmas-draw ./cmd/christmas-draw`, then a `gcr.io/distroless/static` runtime. `WORKDIR /data`, `VOLUME /data`, `EXPOSE 8080`, `ENTRYPOINT ["/christmas-draw"]`, `CMD ["serve","-config","/data/config.yaml","-db","/data/christmas-draw.db"]`.

README sections:
1. **What it does:** the yearly flow.
2. **Quick start:** binary and Docker.
3. **Config reference:** every key, with defaults.
4. **A typical season:** check → SMS test → greetings → reminders → draw (dry-run first) → results.
5. **When someone joins or drops out.**
6. **Making it reachable without opening ports:** Tailscale Funnel (`tailscale funnel 8080`) and Cloudflare Tunnel (`cloudflared tunnel`), and why no router ports are needed.
7. **Secrecy and its limits:** the DB file contains the pairs.
8. **Fresh year:** `reset`.

CLAUDE.md: replace "Project status" with:
- build, test, single-test, and integration-test commands
- the architecture paragraph from this plan's header
- the Global Constraints that guard secrecy and dry-run

- [ ] **Step 1: Write failing tests** (`main_test.go`):
  - `TestCheckOK`: valid config → exit 0, stdout `OK`.
  - `TestCheckProblems`: infeasible config → exit 1, stdout contains `"can only draw from"`.
  - `TestResetNeedsConfirm`: stdin `"no\n"` → DB unchanged. stdin `"reset\n"` → people table empty.
  - `TestAdminLink`: after `Sync` through a temp setup, output is `<base_url>/admin/<16 chars>`.

- [ ] **Step 2: Run** `go test ./cmd/...`. Expected: FAIL.

- [ ] **Step 3: Implement** `main.go`, and write `Dockerfile`, `README.md` and `CLAUDE.md`.

- [ ] **Step 4: Verify:**
  - `go test ./... -race` → PASS.
  - `go vet ./...` → no errors.
  - `CGO_ENABLED=0 go build ./cmd/christmas-draw` → binary produced.
  - `docker build -t christmas-draw .` → succeeds (skip if Docker is unavailable, and say so).

- [ ] **Step 5: Smoke run:** write a temp config with `dry_run: true`, start `serve`, then:
  - `curl` the admin URL → 200
  - `POST /greetings` with `confirm=yes` → `dry-run.log` contains 3 masked lines
  - stop the server

- [ ] **Step 6: Commit** `feat: add CLI, Dockerfile and docs`.
