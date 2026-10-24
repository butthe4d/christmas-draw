package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	christmasdraw "github.com/butthe4d/christmas-draw"
	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
)

const (
	stageCollecting = "collecting"
	stageDelivered  = "delivered"
)

// demoWishes are the sample wish lists, one per person in config.example.yaml.
// Titles include Polish letters and an emoji to show how SMS text is cleaned.
var demoWishes = map[string][]round.ItemInput{
	"Maciej": {
		{Title: "Mechanical keyboard", Link: "https://example.com/keyboard", Note: "Brown switches, please"},
		{Title: "Book: The Martian"},
		{Title: "Warm socks 🧦", Note: "Size 43"},
	},
	"Ania": {
		{Title: "Ceramic teapot", Link: "https://example.com/teapot"},
		{Title: "Yoga mat", Note: "Purple if possible"},
		{Title: "Książka kucharska", Note: "Anything with Italian food"},
	},
	"Tomek": {
		{Title: "Bike lights", Link: "https://example.com/bike-lights"},
		{Title: "Board game: Catan"},
	},
	"Ola": {
		{Title: "Scented candle", Note: "Vanilla or cinnamon"},
		{Title: "Gardening gloves", Note: "Size M"},
	},
	"Kasia": {
		{Title: "Wireless headphones", Link: "https://example.com/headphones"},
		{Title: "Puzzle, 1000 pieces"},
	},
	"Piotr": {
		{Title: "Coffee beans", Note: "Light roast"},
		{Title: "Football scarf", Note: "Any club except the rivals"},
	},
	"Zosia": {
		{Title: "Watercolour paints"},
		{Title: "Sketchbook A4", Link: "https://example.com/sketchbook"},
		{Title: "Hot chocolate mix"},
	},
}

// demoBaseURL turns a listen address like ":8080" into "http://localhost:8080".
func demoBaseURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// seedDemo recreates the demo in dir and returns the round to serve. The
// round always runs in dry-run with a fake gateway, so a demo can never send
// an SMS. Only the demo's own files in dir are replaced.
func seedDemo(ctx context.Context, dir, stage, addr string) (*round.Round, *config.Config, *store.Store, error) {
	base, err := demoBaseURL(addr)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, nil, err
	}
	db := filepath.Join(dir, "christmas-draw.db")
	for _, f := range []string{db, db + "-wal", db + "-shm", dryRunLogPath(db)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), christmasdraw.ExampleConfig, 0o644); err != nil {
		return nil, nil, nil, err
	}
	cfg, err := config.Parse(christmasdraw.ExampleConfig)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.BaseURL = base
	cfg.DryRun = true

	st, err := store.Open(db)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*round.Round, *config.Config, *store.Store, error) {
		st.Close()
		return nil, nil, nil, err
	}
	snd := &sender.Demo{}
	dry := sender.DryRunLog{Path: dryRunLogPath(db)}

	// Seeding walks the real lifecycle with dry-run off, against the fake
	// gateway, so the database is exactly what a real season would leave.
	seedCfg := *cfg
	seedCfg.DryRun = false
	seed := round.New(&seedCfg, st, snd, dry, round.Options{})
	if _, err := seed.Sync(ctx); err != nil {
		return fail(err)
	}
	if _, err := seed.SendGreetings(ctx); err != nil {
		return fail(err)
	}
	people, err := st.People(ctx)
	if err != nil {
		return fail(err)
	}
	for _, p := range people {
		for _, it := range demoWishes[p.Name] {
			if err := seed.AddItem(ctx, p.Token, it); err != nil {
				return fail(err)
			}
		}
	}
	if stage == stageDelivered {
		out, err := seed.Draw(ctx)
		if err != nil {
			return fail(err)
		}
		if len(out.Problems) > 0 {
			return fail(out.Problems[0])
		}
		if _, err := seed.SendResults(ctx); err != nil {
			return fail(err)
		}
	}

	r := round.New(cfg, st, snd, dry, round.Options{})
	if _, err := r.Sync(ctx); err != nil {
		return fail(err)
	}
	return r, cfg, st, nil
}

func runDemo(o options, stdout, stderr io.Writer) int {
	lang := defaultLang
	if o.stage != stageCollecting && o.stage != stageDelivered {
		fmt.Fprintln(stderr, locale.M("cli.demo_bad_stage", "stage", o.stage).In(lang))
		return 2
	}
	failErr := func(err error) int {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r, cfg, st, err := seedDemo(ctx, o.dir, o.stage, o.addr)
	if err != nil {
		var m locale.Msg
		if errors.As(err, &m) {
			fmt.Fprintln(stderr, m.In(lang))
			return 1
		}
		return failErr(err)
	}
	defer st.Close()
	lang = langOf(cfg)

	token, err := r.AdminToken(ctx)
	if err != nil {
		return failErr(err)
	}
	people, err := st.People(ctx)
	if err != nil {
		return failErr(err)
	}
	// The demo prints every private link on purpose: its data is fake and
	// nothing it does reaches a phone.
	fmt.Fprintln(stdout, locale.M("cli.demo_ready", "dir", o.dir, "stage", o.stage).In(lang))
	fmt.Fprintln(stdout, locale.M("cli.admin_page", "url", adminURL(cfg, token)).In(lang))
	fmt.Fprintln(stdout, locale.T(lang, "cli.demo_people", nil))
	for _, p := range people {
		fmt.Fprintln(stdout, locale.M("cli.demo_person", "name", p.Name, "url", cfg.BaseURL+"/p/"+p.Token).In(lang))
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	log.Info("listening", "addr", o.addr, "dry_run", cfg.DryRun)
	if err := listen(ctx, r, cfg, o.addr, log); err != nil {
		return failErr(err)
	}
	return 0
}
