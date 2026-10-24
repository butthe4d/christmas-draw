# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Go is at `/usr/local/go/bin` (`export PATH=/usr/local/go/bin:$PATH`). Go 1.26.8 or newer.

- Build (no cgo): `CGO_ENABLED=0 go build -o christmas-draw ./cmd/christmas-draw`
- All tests: `go test ./... -race`
- Vet: `go vet ./...`
- One package: `go test ./internal/round`
- One test: `go test ./internal/round -run TestName -v`
- Integration test against the gsmservice sandbox (not run by default):
  `GSMSERVICE_SANDBOX_TOKEN=... go test -tags integration ./internal/sender`
- Run (`reset` also deletes `dry-run.log`; `check` lists normalised numbers): `./christmas-draw check|serve|reset|admin-link [-config config.yaml] [-db christmas-draw.db]`
- Demo (fresh sample data in `./demo`, dry-run + fake gateway, prints all links): `./christmas-draw demo [-stage collecting|delivered] [-addr :8080] [-dir demo]`
- Docker: `docker build -t christmas-draw .` (distroless nonroot, uid 65532; `/data` must be writable by it)

## Architecture

One Go binary with subcommands (`cmd/christmas-draw`). The `round` package owns the lifecycle state machine (SETUP, COLLECTING, DRAWN, DELIVERED, CHANGE_PENDING) and every rule; all its exported actions hold one mutex and re-read state inside it. It sits on a SQLite store (`modernc.org/sqlite`, pure Go), a pure draw engine (`draw`), a pure SMS-text/message layer (`smstext`, `message`), and a `Sender` interface (real gsmservice client in `sender`, or the in-memory `sender.Fake` for tests). The `web` package is a thin server-rendered `html/template` layer over `round`. Dry-run is decided inside `round`: it appends to a log file and changes nothing. Config (`config`) is YAML, validated into `locale.Msg` problems. All user-visible text lives in `internal/locale/locales/<lang>.json`.

Batch actions that send SMS (`send` and its callers in `round`) switch to a detached context (`detach`) after the state check, so a client disconnect cannot leave sent SMS unrecorded or the state un-advanced.

The spec is `docs/superpowers/specs/2026-10-23-christmas-draw-design.md`.

## Constraints that guard secrecy and dry-run

- No cgo. All randomness (draw, tokens, SMS test code) comes from `crypto/rand`, injected as `io.Reader` where tests need control.
- Tokens are 10 random bytes as base32, 16 characters. Person links `<base_url>/p/<token>`, admin link `<base_url>/admin/<token>`. Masked token in dry-run logs and previews: 16 `X`.
- Never write a person token, the admin token (except the single start-up line on stdout and `admin-link`), result SMS text, or pairs to normal logs. Don't log request paths.
- The admin page never shows a person's token or result SMS content. Pairs are shown only through Unseal, which records the first view time.
- Dry-run never changes state, never records messages, never saves a draw. Its only side effect is appending to the dry-run log (`dry-run.log` next to the DB). The SMS test is the only send that ignores `dry_run`.
- `check` and `admin-link` must not create or touch the DB file.
- `demo` is the only command that prints person links; it serves with `dry_run` forced on and `sender.Demo` (never contacts a gateway). Its people must match `demoWishes` in `cmd/christmas-draw/demo.go` (`TestExampleConfigShape`).
- SMS text is always transliterated before checks and sending (GSM-7). Segment limits: greeting 2, reminder 2, emergency 2, result `max_result_segments`, SMS test 1.
- Localization: no user-visible literals in Go code or templates. Add each key to both `en.json` and `pl.json` (`TestLocalesComplete` enforces equal key sets). Functions that report problems return `locale.Msg`, never formatted strings.
- Person identity across config edits is the trimmed name (case-sensitive); phones are compared after normalisation.
