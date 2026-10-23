# christmas-draw

A small, self-hosted Secret Santa for a family. Everyone gets a private link to
write a wish list, the app draws who gives to whom (with rules such as "couples
do not draw each other"), and every giver receives an SMS with the person they
drew and a summary of that person's wishes. SMS go out through
[gsmservice](https://gsmservice.pl). One Go binary, one SQLite file.

## 1. What it does

The yearly flow:

1. You list the people (name, phone), couples and extra exclusions in `config.yaml`.
2. The app sends everyone a greeting SMS with a private link (`<base_url>/p/<token>`).
3. Each person writes their wish list on that page. No accounts, no passwords.
4. You send reminders to whoever has not filled in their list.
5. You press "draw". The result is sealed: the admin page shows counts and
   costs, never who drew whom.
6. You press "send results". Each person gets an SMS with their receiver and a
   link to the receiver's full list.

A draw never sends anything by itself, and `dry_run` (on by default) writes SMS
to a log file instead of sending them.

## 2. Quick start

You need an API token from gsmservice (a sandbox token works for trying it out).

### Binary

Go 1.26.8 or newer; no cgo needed.

```sh
CGO_ENABLED=0 go build -o christmas-draw ./cmd/christmas-draw
cp config.example.yaml config.yaml      # edit it
./christmas-draw check                  # validates config, draw feasibility, greeting length
./christmas-draw serve                  # listens on :8080
```

On the first start the admin page URL is printed once to standard output
(`Admin page: <base_url>/admin/<token>`). Keep it private. `./christmas-draw
admin-link` prints it again.

Commands (all accept `-config` (default `config.yaml`) and `-db` (default
`christmas-draw.db`)):

| Command | What it does |
|---|---|
| `serve` (default) | Syncs config into the database and serves the site. `-addr` sets the listen address (default `:8080`). Stops cleanly on Ctrl-C / SIGTERM. |
| `check` | Validates config, lists every person with the number as it will be dialled, checks that a draw is possible, renders the greeting. Prints `OK` or the problems and exits 1. Never touches the database. |
| `reset` | Stop the server first. Wipes the database and `dry-run.log` after you type `reset`. `-yes` skips the prompt. |
| `admin-link` | Prints the admin URL from the database; exits 1 if there is none yet. |

The dry-run log is `dry-run.log`, next to the database file.

### Docker

```sh
docker build -t christmas-draw .
mkdir data && cp config.example.yaml data/config.yaml     # edit it
sudo chown -R 65532:65532 data                           # the container runs as uid 65532
docker run -d --name christmas-draw -p 8080:8080 \
  -e CHRISTMAS_DRAW_SMS_TOKEN=your-token \
  -v "$PWD/data:/data" christmas-draw
docker logs christmas-draw | grep 'Admin page'           # first start only
docker run --rm -v "$PWD/data:/data" christmas-draw admin-link -config /data/config.yaml -db /data/christmas-draw.db
```

The image runs `serve -config /data/config.yaml -db /data/christmas-draw.db`.
The database and the dry-run log live in the `/data` volume. The container
does not run as root (uid 65532), so `/data` must be writable by that user:
`chown 65532:65532` a bind-mounted directory, as above. A named volume
(`-v christmas-draw-data:/data`) inherits the right ownership from the image.

## 3. Config reference

See `config.example.yaml`. Unknown keys are rejected, so typos do not silently
fall back to a default.

| Key | Default | Meaning |
|---|---|---|
| `base_url` | required | Public address, used in SMS links. Must start with `http://` or `https://`. |
| `greeting` | required | Greeting SMS. Must contain `{link}`; `{name}` is available. Max 2 segments. |
| `result_template` | by `language` | Result SMS. Must contain `{receiver}` and `{link}`; `{name}` and `{summary}` available. |
| `reminder_template` | by `language` | Reminder SMS. Must contain `{link}`; `{name}` available. Max 2 segments. |
| `emergency_template` | by `language` | Default text for the "setup changed" notice. Max 2 segments. |
| `exclude_couples` | `true` | Whether people in `couples` are barred from drawing each other. |
| `lists_editable_after_draw` | `false` | Allow editing wish lists after the draw. |
| `dry_run` | `true` | Write SMS to `dry-run.log` instead of sending, and change no state. |
| `organiser_phone` | empty | Receives the SMS test on the admin page. |
| `max_result_segments` | `3` | Longest allowed result SMS, in segments. |
| `language` | `en` | `en` or `pl`. Default web language and default SMS texts. |
| `default_country_code` | empty | e.g. `+48`, prepended to numbers written without a prefix. One leading trunk `0` is dropped first (`07700 900123` with `+44` becomes `+447700900123`). `00` counts as `+`. |
| `people` | required, at least 3 | List of `{ name, phone }`. Names must be unique; phones are normalised to E.164. |
| `couples` | empty | Pairs of names that must not draw each other (when `exclude_couples`). One couple per person. |
| `exclusions` | empty | Extra forbidden pairs, both directions. |
| `sms.provider` | `gsmservice` | The only provider. |
| `sms.token` | required unless `dry_run` is on | API token. `CHRISTMAS_DRAW_SMS_TOKEN` overrides it when set. The SMS test needs it even in dry-run. |
| `sms.sender` | empty | Optional sender name. |
| `sms.server` | `prod` | `prod` or `sandbox`. |
| `sms.price_per_segment` | `0.14` | Used for cost estimates on the admin page. |
| `sms.currency` | `PLN` | Shown next to costs. |

SMS are sent as GSM-7: Polish letters are transliterated (ą becomes a, and so
on). A segment holds 160 characters (153 when a message needs several).
Characters from a wish list that cannot be sent are dropped from the SMS
summary only; the web page shows them intact.

## 4. A typical season

Dry-run has two faces. Greetings in dry-run only write to `dry-run.log`: they
change no state, so the round stays in SETUP and nobody counts as greeted. The
draw needs the round to be in COLLECTING, which only real greetings produce.
Hence the season goes in phases, each ended by editing `dry_run` and
restarting.

1. **Rehearse with `dry_run: true`.** Run `christmas-draw check` and fix what
   it reports; read the numbers it lists. Start the server, open the admin
   page, press the SMS test (set `organiser_phone`; it is the one send that
   ignores `dry_run`, costs one real SMS, and needs `sms.token`). Then send
   the greetings in dry-run and read the texts in `dry-run.log` (tokens are
   masked).
2. **Go live with `dry_run: false`, restart.** Send the greetings for real.
   The round moves to COLLECTING. Send reminders as needed; the draw is
   blocked until everybody has at least one item.
3. **Optional: inspect the result texts.** Set `dry_run: true`, restart, and
   draw. A dry-run draw is thrown away; the result SMS go to `dry-run.log`.
   Note that this log then shows who draws whom. Delete it afterwards if you
   want to stay surprised.
4. **Draw for real.** Set `dry_run: false`, restart, draw. The admin page
   previews the result SMS without dry-run: counts, segments, cost and any
   problems, never the texts or pairs. Review it, then send the results. Use
   "resend" for anyone whose SMS failed or whose number was wrong. Delivery
   status is shown per person.

Only after the result SMS go out do people see who they drew on their page.

## 5. When someone joins or drops out

Edit `config.yaml` and restart. The app compares it with the database:

- A fixed phone number is just stored. Resend that person's last SMS from the admin page.
- Added or removed people before the draw: others keep their wish lists; the
  newcomer gets a greeting.
- Changes after a draw but before results: the sealed draw is discarded, and
  you draw again once the newcomer has a list.
- Changes after results were sent: the round goes to "change pending". The
  admin page asks you to review and send an emergency SMS to everyone; wish
  lists are kept and you draw again. If that SMS fails for someone, the admin
  page shows "Resend change notice" next to them until the next draw.

Cosmetic edits (`"Ania "` vs `"Ania"`, `600 100 200` vs `+48600100200`) are not
changes: a person is identified by their trimmed name.

## 6. Making it reachable without opening ports

Relatives need to open the links from outside your network. A home server
behind a router or CGNAT cannot be reached from the internet directly, and you
do not need to change that. Both tools below make an outbound connection from
your server to a provider, who then forwards visitors to it. Nothing listens on
your router, so no port forwarding is needed.

- **Tailscale Funnel:** `tailscale funnel 8080` publishes
  `https://<machine>.<tailnet>.ts.net` and forwards it to port 8080. Set
  `base_url` to that address.
- **Cloudflare Tunnel:** `cloudflared tunnel --url http://localhost:8080` gives
  a temporary public address; for a stable name create a named tunnel on your
  own domain. Set `base_url` to it.

Use `https://` in `base_url`. Anyone with a link can open that person's page,
so links are random 16-character tokens and are only sent by SMS.

## 7. Secrecy and its limits

- Pairs are stored in the database so each person can be shown who they drew.
  The admin page never shows them, except through "unseal", which asks for
  confirmation and permanently records when the results were viewed.
- Neither the admin page nor the logs show result SMS texts, person tokens or
  pairs. Request paths are not logged.
- Dry-run writes full SMS text, including the receiver, to `dry-run.log`
  (tokens are masked). If you want to be surprised too, do not read it.
- This protects against casual or accidental peeking. **It does not protect
  against anyone who can read the SQLite file or the dry-run log on the host.**
  The database contains the pairs. Whoever has access to the machine can see them.
- The admin URL is the organiser's key. Do not share it.

## 8. Fresh year

```sh
./christmas-draw reset      # type "reset" to confirm; -yes skips the prompt
```

Stop the server first. This wipes people, wish lists, the draw, sent-message
records and the admin token, and deletes `dry-run.log` next to the database
(it may contain pairs). The next `serve` creates new tokens and prints a new admin URL.

## 9. Adding a language

All user-visible text lives in `internal/locale/locales/`. Copy `en.json` to
`<code>.json` (e.g. `de.json`), translate the values (keep the `{placeholders}`),
and rebuild. `go test ./internal/locale` runs `TestLocalesComplete`, which fails
if any key is missing. SMS templates (`sms.*`) must stay within GSM-7
after transliteration. The new language then appears in the language switcher
and as a value for `language`.

## Development

```sh
go test ./... -race
go vet ./...
go test -tags integration ./internal/sender   # needs GSMSERVICE_SANDBOX_TOKEN
```

Licensed under Apache 2.0.
