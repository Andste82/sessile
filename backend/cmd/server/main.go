// Command server is the sessile backend: a persistent terminal session manager.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Andste82/sessile/backend/internal/api"
	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/serverconfig"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/sshpty"
	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/upgrade"
	"github.com/Andste82/sessile/backend/internal/ws"
	"github.com/Andste82/sessile/backend/web"
)

// hostResolver implements session.HostResolver over the hosts registry, for
// Restart's re-resolution of an SSH session's current host config (§12b
// M17) — the only reason session.HostResolver exists as an interface rather
// than a direct dependency on internal/hosts, which would otherwise be a
// clean import for internal/session to take. Scoped to userID exactly like
// every other host lookup (§4.5, §6) — never a client-supplied id.
type hostResolver struct {
	registry *hosts.Registry
}

func (r *hostResolver) Resolve(userID, hostID string) (sshpty.Target, string, error) {
	store, err := r.registry.For(userID)
	if err != nil {
		return sshpty.Target{}, "", err
	}
	host, found := store.Get(hostID)
	if !found {
		return sshpty.Target{}, "", session.ErrHostNotFound
	}
	return host.SSHTarget(), host.Name, nil
}

func main() {
	args := os.Args[1:]
	cmd := run
	if len(args) > 0 && args[0] == adoptCommand {
		args = args[1:]
		cmd = func(a []string) error { return runAdopt(a, os.Stdout, os.Stderr) }
	}
	if err := cmd(args); err != nil {
		// --version and --help are requests, not failures: they must exit 0 and
		// must not be reported as "fatal". flag has already printed the usage
		// text by the time ErrHelp surfaces.
		switch {
		case errors.Is(err, config.ErrVersionRequested):
			fmt.Println("sessile", config.Version)
			return
		case errors.Is(err, flag.ErrHelp):
			return
		}
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	log.Info("starting sessile",
		"version", config.Version, "data-dir", cfg.DataDir, "workspace-dir", cfg.WorkspaceDir,
		"addr", cfg.Addr)

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("load embedded frontend: %w", err)
	}

	// Before anything else writes to the data directory: say so loudly if it
	// holds nothing, refuse one a newer release has already upgraded, and
	// back it up before migrating it (§8, §12e). Every later step may create
	// or rewrite files in it.
	if upgrade.LooksFresh(cfg.DataDir) {
		log.Warn("data directory is empty: starting as a new install. "+
			"Fine on a first start. If this is an upgrade, STOP: your data is not where the server is looking. "+
			"Check the volume mounts (docker inspect <container> --format '{{json .Mounts}}') "+
			"and see Upgrading in the README before creating an admin account",
			"data-dir", cfg.DataDir)
	}
	// Open the metadata store; it migrates the schema (backing up first) and
	// reconciles any session left "running" by a previous process to
	// "stopped" on open (§8).
	store, state, err := openStore(cfg.DataDir, cfg.DB, log)
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("store ready", "db", cfg.DB, "schema-version", storage.SchemaVersion())
	if n, err := store.CountOwnerless(); err != nil {
		log.Error("count ownerless sessions failed", "err", err)
	} else if n > 0 {
		log.Warn("sessions without an owner are invisible to every user. "+
			"They predate accounts (0.3.x and older). To hand them to a user, stop the server and run "+
			"'sessile adopt-sessions --data-dir=<data-dir> --user=<username>' "+
			"(Docker: docker compose run --rm sessile adopt-sessions --data-dir=/config --user=<username>)",
			"count", n, "data-dir", cfg.DataDir)
	}

	if state.LastVersion != "" && state.LastVersion != config.Version {
		log.Info("version changed since the last start", "from", state.LastVersion, "to", config.Version)
	}
	if err := upgrade.WriteState(cfg.DataDir, upgrade.State{
		LastVersion:   config.Version,
		LayoutVersion: upgrade.LayoutVersion,
	}); err != nil {
		return err
	}

	serverCfg, err := serverconfig.Open(filepath.Join(cfg.DataDir, "config.yml"))
	if err != nil {
		return fmt.Errorf("open config.yml: %w", err)
	}

	users, err := auth.OpenUsers(filepath.Join(cfg.DataDir, "users.yml"))
	if err != nil {
		return fmt.Errorf("open users.yml: %w", err)
	}
	if users.Count() == 0 {
		log.Info("no users yet — the server is unlocked; the first login creates the admin account")
	}
	webSessions := auth.NewSessionStore(auth.DefaultSessionTTL)
	defer webSessions.Stop()

	hostsRegistry := hosts.NewRegistry(cfg.DataDir)

	manager := session.NewManager(cfg.WorkspaceDir, cfg.Shells, cfg.BufferSize, cfg.DataDir, store, log)
	manager.SetHostResolver(&hostResolver{registry: hostsRegistry})
	// Discard long-idle stopped sessions before anything can attach to them.
	// Off unless --session-retention is set.
	if _, err := manager.PruneStopped(cfg.SessionRetention); err != nil {
		log.Error("prune stopped sessions failed", "err", err)
	}
	wsHandler := ws.NewHandler(manager, cfg, log)

	srv := api.NewServer(cfg, manager, wsHandler, log, cfg.WorkspaceDir, serverCfg, users, webSessions, hostsRegistry)
	handler := srv.Router(dist)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Run the HTTP server until a signal arrives.
	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", "err", err)
	}
	// Terminate shell processes and mark sessions stopped (§4.6).
	manager.Shutdown()
	log.Info("shutdown complete")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
