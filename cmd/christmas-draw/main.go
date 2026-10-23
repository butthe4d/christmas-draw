// Command christmas-draw runs the family gift draw: serve, check, reset, admin-link.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/butthe4d/christmas-draw/internal/config"
	"github.com/butthe4d/christmas-draw/internal/draw"
	"github.com/butthe4d/christmas-draw/internal/locale"
	"github.com/butthe4d/christmas-draw/internal/message"
	"github.com/butthe4d/christmas-draw/internal/round"
	"github.com/butthe4d/christmas-draw/internal/sender"
	"github.com/butthe4d/christmas-draw/internal/store"
	"github.com/butthe4d/christmas-draw/internal/web"
)

const defaultLang = "en"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	config, db, addr string
	yes              bool
}

// run is main without os.Exit, so tests can drive it.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve", "check", "reset", "admin-link":
	default:
		fmt.Fprintln(stderr, locale.M("cli.unknown_command", "command", cmd).In(defaultLang))
		fmt.Fprintln(stderr, locale.T(defaultLang, "cli.usage", nil))
		return 2
	}
	var o options
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.config, "config", "config.yaml", "path to the config file")
	fs.StringVar(&o.db, "db", "christmas-draw.db", "path to the database file")
	switch cmd {
	case "serve":
		fs.StringVar(&o.addr, "addr", ":8080", "listen address")
	case "reset":
		fs.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch cmd {
	case "check":
		return runCheck(o, stdout, stderr)
	case "reset":
		return runReset(o, stdin, stdout, stderr)
	case "admin-link":
		return runAdminLink(o, stdout, stderr)
	}
	return runServe(o, stdout, stderr)
}

// loadConfig loads and validates the config. On failure it prints every
// problem in the config's language (English when it did not parse) and
// returns ok=false.
func loadConfig(path string, stdout io.Writer) (*config.Config, bool) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, true
	}
	lang := langOf(cfg)
	var ps config.Problems
	var m locale.Msg
	switch {
	case errors.As(err, &ps):
		for _, p := range ps {
			fmt.Fprintln(stdout, p.In(lang))
		}
	case errors.As(err, &m):
		fmt.Fprintln(stdout, m.In(lang))
	default:
		fmt.Fprintln(stdout, locale.M("cli.error", "error", err.Error()).In(lang))
	}
	return cfg, false
}

func langOf(cfg *config.Config) string {
	if cfg != nil && locale.Has(cfg.Language) {
		return cfg.Language
	}
	return defaultLang
}

// quietLang is the language for messages when the config is not needed.
func quietLang(path string) string {
	cfg, _ := config.Load(path)
	return langOf(cfg)
}

func runCheck(o options, stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(o.config, stdout)
	if !ok {
		return 1
	}
	lang := langOf(cfg)
	var problems []locale.Msg

	// Let the organiser verify every number as it will be dialled.
	for _, p := range cfg.People {
		phone, err := config.NormalizePhone(p.Phone, cfg.DefaultCountryCode)
		if err != nil {
			phone = p.Phone
		}
		fmt.Fprintln(stdout, locale.M("cli.person_line", "name", p.Name, "phone", phone).In(lang))
	}

	names := make([]string, len(cfg.People))
	for i, p := range cfg.People {
		names[i] = p.Name
	}
	rules := draw.NewRules(names, cfg.Couples, cfg.Exclusions, cfg.ExcludeCouples)
	if m := round.InfeasibleMsg(draw.Feasible(rules)); m != nil {
		problems = append(problems, *m)
	}
	if len(cfg.People) > 0 {
		link := message.Link(cfg.BaseURL, message.MaskedToken)
		vars := map[string]string{"name": cfg.People[0].Name, "link": link}
		if _, err := message.Render(cfg.Greeting, vars, 2); err != nil {
			problems = append(problems, round.MessageProblem("greeting", err))
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p.In(lang))
		}
		return 1
	}
	fmt.Fprintln(stdout, locale.T(lang, "cli.ok", nil))
	return 0
}

// dryRunLogPath is the dry-run log, next to the database.
func dryRunLogPath(db string) string { return filepath.Join(filepath.Dir(db), "dry-run.log") }

func runReset(o options, stdin io.Reader, stdout, stderr io.Writer) int {
	lang := quietLang(o.config)
	if !o.yes {
		fmt.Fprint(stdout, locale.M("cli.reset_warning", "db", o.db).In(lang)+" ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if strings.TrimSpace(line) != "reset" {
			fmt.Fprintln(stdout, locale.T(lang, "cli.reset_aborted", nil))
			return 1
		}
	}
	st, err := store.Open(o.db)
	if err != nil {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	defer st.Close()
	if err := st.Reset(context.Background()); err != nil {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	// The dry-run log may hold draw pairs: a fresh year starts without it.
	if err := os.Remove(dryRunLogPath(o.db)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	fmt.Fprintln(stdout, locale.T(lang, "cli.reset_done", nil))
	return 0
}

func adminURL(cfg *config.Config, token string) string {
	return strings.TrimRight(cfg.BaseURL, "/") + "/admin/" + token
}

func runAdminLink(o options, stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(o.config, stdout)
	if !ok {
		return 1
	}
	lang := langOf(cfg)
	none := func() int {
		fmt.Fprintln(stderr, locale.T(lang, "cli.admin_link_none", nil))
		return 1
	}
	if _, err := os.Stat(o.db); err != nil {
		return none() // do not create an empty database just to look
	}
	st, err := store.Open(o.db)
	if err != nil {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	defer st.Close()
	token, err := round.New(cfg, st, nil, sender.DryRunLog{}, round.Options{}).AdminToken(context.Background())
	if errors.Is(err, store.ErrNotFound) {
		return none()
	}
	if err != nil {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	fmt.Fprintln(stdout, locale.M("cli.admin_page", "url", adminURL(cfg, token)).In(lang))
	return 0
}

func runServe(o options, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	cfg, ok := loadConfig(o.config, stdout)
	if !ok {
		return 1
	}
	lang := langOf(cfg)
	fail := func(err error) int {
		fmt.Fprintln(stderr, locale.M("cli.error", "error", err.Error()).In(lang))
		return 1
	}
	st, err := store.Open(o.db)
	if err != nil {
		return fail(err)
	}
	defer st.Close()

	snd := sender.NewGSMService(cfg.SMS.Token, cfg.SMS.Sender, cfg.SMS.Server)
	dry := sender.DryRunLog{Path: dryRunLogPath(o.db)}
	r := round.New(cfg, st, snd, dry, round.Options{})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := r.Sync(ctx)
	if err != nil {
		return fail(err)
	}
	if rep.AdminTokenCreated {
		// The one place the admin URL is shown on start-up: stdout, never the log.
		token, err := r.AdminToken(ctx)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, locale.M("cli.admin_page", "url", adminURL(cfg, token)).In(lang))
	}

	srv := &http.Server{
		Addr:              o.addr,
		Handler:           web.NewServer(r, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // sending a batch of SMS can take a while
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 16,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", o.addr, "dry_run", cfg.DryRun)

	select {
	case err := <-errc:
		return fail(err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fail(err)
	}
	return 0
}
