# Christmas Draw: Design

Date: 2026-10-23
Status: draft, awaiting approval

## Intent

Each adult in a family gives a present to exactly one other adult and gets a
present from exactly one other adult. Before Christmas, everyone writes a wish
list. A draw decides who gives to whom. Each person then gets an SMS telling
them who they drew and what that person wants.

The author already has a private app for this. In that app, people are
hardcoded and the author types in everyone's presents by hand. This project
replaces it with an app that:

- other families can run by downloading the published code, writing one config
  file, and starting it
- lets each person fill in their own wish list through a private link
- keeps the draw secret, even from the organiser, who is also a participant

The code is published (open source). It is **not** a hosted service for other
families.

## Goals

- The organiser configures only the people, couples, greeting text, and SMS
  credentials.
- Everyone gets the same configured greeting by SMS, along with their personal
  link to fill in a wish list.
- People fill in their own wish lists in a browser. They need no account and no
  password.
- The draw respects the configured rules, and the app proves it before any SMS
  is sent.
- Draw results go out by SMS: who you drew, a short summary of their wishes, and
  a link to the full list.
- Hosting costs nothing. It runs on a homelab, made public through a tunnel.
- It is easy for strangers to run: one binary or Docker image, plus one config
  file.

## Non-goals

- Hosting the app for other families, or supporting several families in one
  instance.
- Keeping history across years. Each Christmas is a fresh start.
- "Avoid last year's pair" rules.
- User accounts, passwords, or email.
- Children, or any present-giving structure other than one giver and one
  receiver per adult.

## Anti-goals (would make it a failure even if it works)

- The organiser can casually see who drew whom.
- SMS go out with a broken draw (someone drew themselves or their partner,
  someone gets two givers or none).
- SMS go out without a way to check first, because SMS cost real money and
  can't be taken back.
- The app needs open ports on the home router.

## Constraints

- **SMS provider:** gsmservice / szybkisms.pl, which the author already uses
  (about 0.14 PLN per SMS). The official Go SDK is
  `github.com/gsmservice-pl/messaging-sdk-go/v4`. It has `Outgoing.Sms.Send`,
  `Outgoing.Sms.GetPrice`, and `Outgoing.GetByIds` (for delivery status), plus
  a `sandbox` server option (`https://api.szybkisms.pl/rest-sandbox`).
- **SMS encoding:** Polish diacritics are transliterated to plain ASCII
  (ą→a, ć→c, ę→e, ł→l, ń→n, ó→o, ś→s, ź→z, ż→z, plus capitals). This keeps every
  SMS in the GSM-7 alphabet: 160 characters for a single SMS, 153 per segment
  when joined. The draw SMS may span 1–3 segments.
- **Location:** Poland. Phone numbers are Polish mobile numbers (`+48` and 9
  digits). Accepting other countries is left to the builder, but Polish is the
  case that must work.
- **Hosting:** the author's homelab behind an Orange Światłowód / Funbox router,
  possibly behind CGNAT. No inbound ports. Public access only through an
  outbound tunnel: Tailscale Funnel (free `*.ts.net` address, no domain needed)
  or Cloudflare Tunnel (needs a domain on Cloudflare). The tunnel provides
  HTTPS.

## Approaches considered

| Approach | Trade-offs | Outcome |
|---|---|---|
| **A. Go single binary, server-rendered HTML, SQLite, homelab + tunnel** | 0 PLN hosting. One file to run, which is ideal for strangers self-hosting. Low memory (Raspberry Pi is enough). Official gsmservice Go SDK. The author wants to learn Go. Cost: learning curve, and hand-written CSS. | **Chosen** |
| B. Python (FastAPI), same architecture | Easier to read for a newcomer, great testing tools. But dependency and version management makes Docker the only realistic install for others, and it uses more memory. | Rejected |
| C. Serverless (Cloudflare Workers + KV) | Free, nothing at home. But the code is tied to one vendor, every family needs a Cloudflare account, and there is no gsmservice SDK, so the API would be called by hand. Works against easy self-hosting. | Rejected |

The author chose A.

## Decisions made with the author

1. **SMS is mandatory**, with Polish letters transliterated to ASCII.
2. **Draw SMS = name of the drawn person + short wish summary + link** to their
   full wish list, in 1–3 SMS. The full list lives on a web page, which can be
   nicer than a plain list ("letter to Santa").
3. **The draw is sealed.** The organiser doesn't see the pairs. There is an
   emergency "unseal" for when something went wrong (see Secrecy).
4. **Checks run before any SMS is sent** (see Validation), plus automated tests
   that look for irregularities.
5. **Dry-run mode** writes messages to a log instead of sending them, like the
   author's old app.
6. **The draw is blocked** until every person has at least one item on their
   wish list.
7. **Couple exclusion is a config toggle**, on by default.
8. **Wish lists can be edited after the draw:** a config toggle, off by default
   (frozen at the moment of the draw).
9. **Fresh start every year.** No history.
10. **Go**, single binary, approach A.

## Proposals the author has heard and not rejected

These came up in the conversation as proposals. Treat them as part of the
design unless the author changes them.

- **Wish list item fields:** title (required), optional shop link, optional
  note (size, colour, "anything by this author"). The SMS summary uses titles
  only.
- **Private link = password.** Each person's link has a random token. Anyone
  with the link can edit that person's list and, after the draw, see who they
  drew. Lost SMS can be resent.
- **Admin access** is through a separate admin link with its own token,
  generated on first start. No accounts.
- **"Send reminder" button:** sends an SMS only to people whose list is still
  empty. Costs one SMS each, and is pressed manually.
- **General exclusions:** besides couples, the config can list pairs who must
  never draw each other (e.g. a parent and an adult child living together). A
  couple is just the most common exclusion. The couple toggle controls only the
  `couples` list. `exclusions` always apply.
- **Web UI in Polish and English.** SMS texts and greeting come from config, so
  they are in whatever language the organiser writes.

## How it works

### Lifecycle

One instance runs one draw. State moves forward, except for the changes in "Fixing things mid-round":

```
SETUP ──send greetings──▶ COLLECTING ──draw──▶ DRAWN ──send results──▶ DELIVERED
  ▲                                                                        │
  └──────────────────────── reset (CLI, fresh year) ───────────────────────┘
```

- **SETUP:** the app has loaded the config and created a token for each person.
  The organiser opens the admin page, checks the config summary and the draw
  feasibility check, and previews the greeting SMS.
- **COLLECTING:** greetings were sent. People fill in their lists. The admin
  page shows who has filled in their list and who hasn't, a reminder button,
  and a per-person "resend greeting" button.
- **DRAWN:** the draw has been made and validated, and the result is sealed.
  The admin page shows a preview of the result SMS: the *count* of messages,
  segments, and total cost, but not the contents, because the contents would
  reveal the pairs. The organiser confirms to send.
- **DELIVERED:** result SMS were sent. The admin page shows delivery status per
  person (from `GetByIds`) and a per-person "resend result" button. That
  button sends without showing the content.

Draw and send-results are separate steps, so validation and the preview can sit
between them.

### Fixing things mid-round (decided with the author)

Wish lists are never thrown away just because someone else's details changed.
On start, the app compares the config with the database. Changes that are
allowed are applied; anything else is refused with an explanation.

| Change | Allowed when | What happens |
|---|---|---|
| Phone number fixed | any time | Stored; the organiser can resend that person's last SMS. Nothing else changes. |
| Person added | SETUP, COLLECTING | New token, new greeting for that person only. Others keep their lists. |
| Person removed | SETUP, COLLECTING | Their list and token are deleted. Others keep their lists. |
| Couples / exclusions changed | SETUP, COLLECTING | Feasibility re-checked. |
| Person added or removed | DRAWN (results not sent yet) | The sealed draw is discarded. A newcomer gets a greeting and must fill in a list; then the organiser draws again. Nobody else gets an SMS, since nobody has seen a result yet. |
| Person added or removed | DELIVERED | "Setup changed" flow, below. |

**"Setup changed" flow (after result SMS went out).** Wish lists are kept.

1. The organiser edits the config (removes or adds people, fixes couples or
   exclusions) and restarts. The app sees that results were already sent and
   puts the round into a *change pending* state. Nothing is sent yet.
2. The admin page re-runs the config and feasibility checks for the new group.
   If a draw is impossible, it says why and the organiser fixes the config.
3. The organiser writes the emergency SMS on the admin page, right before
   sending it. It is prefilled with a default ("Zmiana w losowaniu! Poprzednie
   losowanie jest niewazne, wkrotce dostaniesz nowe."), and `{name}` and
   `{link}` are available. The page shows the usual GSM-7, length, and cost
   checks, plus the dry-run option.
4. Confirm: the old draw is discarded, and the emergency SMS goes to **everyone
   still in the group**. A newcomer gets the greeting instead. The removed
   person gets nothing; their link stops working and their list is deleted.
5. The round is back in COLLECTING. The draw is blocked until the newcomer's
   list has at least one item, as usual. Then: draw, validate, preview, and new
   result SMS to everyone.

The full CLI `reset` is still there for wiping everything and starting over.

Other config changes that don't fit the table are refused, with an
explanation.

### Configuration

One YAML file. Its shape, as discussed with the author:

```yaml
base_url: "https://swieta.example.ts.net"   # used to build links in SMS
greeting: "Hej {name}! W tym roku znow losujemy prezenty. Wpisz swoje zyczenia: {link}"
result_template: "Wylosowales: {receiver}. Zyczenia: {summary} Pelna lista: {link}"   # optional, has a default
reminder_template: "..."                     # optional, has a default
exclude_couples: true
lists_editable_after_draw: false
dry_run: true
organiser_phone: "+48..."                     # receives the SMS test
max_result_segments: 3
language: pl                                  # web UI default; en also available
people:
  - { name: Maciej, phone: "+48..." }
  - { name: Ania,   phone: "+48..." }
couples:
  - [Maciej, Ania]
exclusions:
  - [Ania, Tomek]
sms:
  provider: gsmservice
  token: "..."             # or from an environment variable
  sender: "..."            # optional sender name
  server: prod             # prod | sandbox
```

Placeholders: `{name}` and `{link}` in the greeting and reminder;
`{receiver}`, `{summary}`, and `{link}` in the result. All SMS text is
transliterated before sending. An SMS containing a character outside GSM-7
after transliteration is a validation error, not a silent replacement.

### Secrecy

- The pairs are stored in the database so the app can show each giver who they
  drew. The admin UI never shows them.
- **Unseal:** the admin page has an "unseal" action behind a confirmation. It
  shows all pairs and permanently records the time. From then on, the admin page
  shows "results were viewed on <date> at <time>" for the rest of the draw.
- This protects against casual or accidental peeking. It doesn't protect
  against someone reading the SQLite file directly on the host. The README says
  so plainly.
- Result SMS contents never appear on the admin page or in normal logs. In
  dry-run mode, they go to a separate dry-run log file and the admin page says
  where it is. The organiser chooses dry-run knowing they may see pairs there.
  Builder's call: the dry-run log could hide or replace the receiver, or show
  everything; showing everything matches the author's old app.

### The draw

- Each person gives to exactly one other person and receives from exactly one
  other person, i.e. a permutation with no fixed points.
- Forbidden pairs: self, couples (if `exclude_couples`), and `exclusions`. Both
  directions are forbidden.
- **Feasibility:** before the draw, and already in SETUP, the app checks
  whether a valid assignment exists. It treats this as a bipartite matching
  between givers and receivers. If none exists, it explains why in plain words
  (e.g. "3 people, 2 of them a couple: the couple both have only one person left
  to draw").
- **Randomness:** `crypto/rand`. The draw should be uniformly random over valid
  assignments, or close to it. Rejection sampling (shuffle, check, retry) is
  fine for family sizes. Randomized backtracking is fine too.
- Whether the result must be one big cycle (A→B→C→…→A) is not required. Any
  valid permutation is fine.

### Validation before SMS

Run automatically at the relevant step. Any failure blocks the step and is
shown on the admin page.

1. **Config:** valid Polish phone numbers, no duplicate names or phones,
   couples and exclusions refer to configured people, nobody in two couples,
   at least 3 people (2 people can only draw each other, which is pointless but
   valid; the builder decides whether to allow it).
2. **Feasibility:** a valid draw exists (see above).
3. **Draw invariants:** checked after every draw, before the result is saved.
   Nobody drew themselves, nobody drew a forbidden pair, everyone gives once,
   everyone receives once.
4. **Messages:** every SMS is GSM-7 after transliteration, fits its segment
   limit (greeting and reminder: the builder decides, probably 1–2; result:
   `max_result_segments`). The admin page shows the total number of messages,
   segments, and estimated cost before any send. Cost comes from
   `Outgoing.Sms.GetPrice` if it works, or from a per-segment price in config
   as a fallback.
5. **Wish lists:** the draw is blocked until every person has at least one item.

**Dry-run:** when `dry_run: true`, every send writes the full message (phone,
text, segment count) to the dry-run log instead of calling the provider.
`server: sandbox` (the provider's test system) is also available.

**SMS test (requested by the author):** an admin action that sends one *real*
SMS to the organiser's own number (`organiser_phone` in config), even when
`dry_run` is on, to prove the provider, token, and sender name work. The text
is semi-random so each test is distinguishable, e.g. `christmas-draw test
21:03 kod 4821`. The admin page shows the provider's response and then the
delivery status from `GetByIds`. Available in every state. It is the only send
that ignores dry-run.

### Result SMS summary

The summary is built from item titles, joined with commas, in list order. Items
are added until the whole SMS would exceed `max_result_segments`, then cut with
"…". The link is always included. If even the name plus link doesn't fit, it's
a validation error.

Links should be short. The token in the URL is about 16 characters (around
80 bits, base32), e.g. `https://swieta.example.ts.net/p/AB12CD34EF56GH78`.

### Web pages

- **Person page** (`/p/<token>`):
  - Before the draw: edit your own wish list.
  - After the draw: your list (read-only unless `lists_editable_after_draw`),
    plus who you drew and their full list, styled as a "letter to Santa".
  - Polish/English switch.
- **Admin page** (`/admin/<token>`): state, checks, previews, and the actions
  above.
- Server-rendered Go `html/template`, hand-written CSS, works on a phone. Little
  or no JavaScript. Builder may add htmx or similar if it clearly helps.

### Data

SQLite, one file. Roughly: people (name, phone, token), wish list items (person,
title, link, note, order), draw (giver → receiver), state (lifecycle stage,
unseal time), messages sent (person, type, provider message id, status, sent
time). The exact schema is the builder's call.

**Reset:** a CLI command (e.g. `christmas-draw reset`) asks for confirmation,
then wipes the database for a fresh year. New tokens are created on the next
start.

### Running it

- One Go binary, plus a Docker image.
- Commands roughly like: `serve` (default), `reset`, `check` (validate config
  and feasibility without starting), `admin-link` (print the admin URL).
- The admin token is created on first start, stored in the database, and
  printed to the log once. `admin-link` prints it again.
- The README covers: writing the config, running with Docker or the binary, and
  making it public with Tailscale Funnel or Cloudflare Tunnel. It says outright
  that no router ports need opening.

### Calls made after the builder review

These were settled by Claude, not the author. The author can override them.

- **Dry-run never changes state.** A dry-run greeting, draw, or result send
  writes to the dry-run log and nothing else: no state change, no saved draw,
  no recorded messages. A dry-run draw is thrown away. So switching
  `dry_run` off needs no reset.
- **Dry-run logs mask tokens.** Links in the dry-run log show a placeholder
  (e.g. `/p/XXXXXXXX`), so the log never gives the organiser a working link
  to someone else's page.
- **The admin page never shows or copies a person's token.** Previews use a
  placeholder token. A lost link is fixed by resending the SMS.
- **A person's page shows who they drew only from DELIVERED on**, not at
  DRAWN. Nobody sees a pairing that could still change before results are
  sent.

## Testing

- **Draw:** property tests run the draw thousands of times on tricky setups
  (few people, many couples, many exclusions, barely feasible, infeasible) and
  check the invariants each time. Infeasible setups are always detected.
- **SMS:** transliteration, GSM-7 detection, segment counting (160 / 153),
  summary trimming.
- **Flow:** HTTP handler tests with a fake SMS sender behind an interface. They
  cover the lifecycle, blocking rules, token checks, frozen lists, and unseal
  recording.
- **Setup changed:** tests for adding and removing people in every state.
  Other people's lists survive, the emergency SMS goes to exactly the
  remaining group, the removed person's link stops working, and the new draw
  passes the invariants.
- **Provider:** an optional integration test against the gsmservice sandbox,
  skipped unless a token is set.

## Left to the builder

- Exact database schema, package layout, routing library (standard library is
  fine).
- Default SMS templates (Polish, ASCII-only) and segment limits for greeting and
  reminder.
- Whether to accept non-Polish phone numbers.
- What exactly goes in the dry-run log (see Secrecy).
- Rate limiting or other hardening of token endpoints. Tokens are long and
  random, so the minimum is "don't leak them in logs".
- Visual design of the pages, within "festive, readable on a phone".
