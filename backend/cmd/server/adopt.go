package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/serverconfig"
	"github.com/Andste82/sessile/backend/internal/upgrade"
)

// adoptCommand is the subcommand name: `sessile adopt-sessions …`.
const adoptCommand = "adopt-sessions"

const adoptUsage = `Usage: sessile adopt-sessions --data-dir=<dir> --user=<username>

Hands every session without an owner to one user. Sessions created before
accounts existed (0.3.x and older) have no owner, and every session belongs
to exactly one user, so nobody can see them until they are adopted.

Run it while the server is stopped. It backs up the data directory to
<data-dir>/backups/ first and prints how many sessions it changed; a second
run finds none and changes nothing. With Docker Compose:

  docker compose stop sessile
  docker compose run --rm sessile adopt-sessions --data-dir=/config --user=<username>
  docker compose start sessile

Flags:
`

// runAdopt is the adopt-sessions subcommand (§12e M34). It is deliberately a
// command and not an API route: who owns a session is never something a
// request decides (§4.3, §10), and whoever can run this against the data
// directory is the host's operator, trusted with all of it already (§11).
// The user is resolved by name through users.yml, never taken as an id.
func runAdopt(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("sessile "+adoptCommand, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprint(errOut, adoptUsage)
		fs.PrintDefaults()
	}
	defaultDataDir := "./data"
	if v, ok := os.LookupEnv("TSM_DATA_DIR"); ok {
		defaultDataDir = v
	}
	dataDir := fs.String("data-dir", defaultDataDir, "the server's data directory (the one holding sessions.db and users.yml)")
	username := fs.String("user", "", "username from users.yml to hand the sessions to (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (see --help)", fs.Arg(0))
	}
	if *username == "" {
		return errors.New("--user is required (see --help)")
	}

	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("resolve data-dir: %w", err)
	}
	// Nothing below may create files: pointed at the wrong directory, this
	// must say so and leave it as it was, not turn it into a new install.
	dbPath := filepath.Join(dir, "sessions.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no sessions.db in %s — is --data-dir the server's data directory? (%w)", dir, err)
	}
	usersPath := filepath.Join(dir, "users.yml")
	if _, err := os.Stat(usersPath); err != nil {
		return fmt.Errorf("no users.yml in %s, so there are no accounts to hand sessions to yet. "+
			"Start the server once, create the admin account, stop it, and run this again (%w)", dir, err)
	}
	users, err := auth.OpenUsers(usersPath)
	if err != nil {
		return err
	}
	user, found := users.FindByUsername(*username)
	if !found {
		var names []string
		for _, u := range users.List() {
			names = append(names, u.Username)
		}
		return fmt.Errorf("no user %q in %s (users: %s)", *username, usersPath, strings.Join(names, ", "))
	}

	log := slog.New(slog.NewTextHandler(errOut, nil))
	store, _, err := openStore(dir, dbPath, log)
	if err != nil {
		return err
	}
	defer store.Close()

	pending, err := store.CountOwnerless()
	if err != nil {
		return err
	}
	if pending == 0 {
		fmt.Fprintf(out, "No sessions without an owner in %s. Nothing changed.\n", dbPath)
		return nil
	}

	backup, err := upgrade.Backup(dir, "adopt", store.Snapshot, time.Now())
	if errors.Is(err, upgrade.ErrPruneFailed) {
		log.Warn("could not remove old backups", "err", err)
	} else if err != nil {
		return fmt.Errorf("back up before adopting (nothing was changed): %w", err)
	}
	adopted, err := store.AdoptOwnerless(user.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Adopted %d session(s) for %s. Backup from before: %s\n", adopted, user.Username, backup)

	// Every ownerless session is a local shell — SSH targets arrived together
	// with accounts — and restarting one needs allowLocalHost (§9).
	cfgPath := filepath.Join(dir, "config.yml")
	if _, err := os.Stat(cfgPath); err == nil {
		if cfg, err := serverconfig.Open(cfgPath); err == nil && !cfg.Get().AllowLocalHost {
			fmt.Fprintln(out, "Note: these are local-shell sessions, and allowLocalHost is off. "+
				"Turn it on in Settings (as admin) or in config.yml to restart them.")
		}
	}
	return nil
}
