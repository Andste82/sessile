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
	if err := run(os.Args[1:]); err != nil {
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
		"addr", cfg.Addr, "dev", cfg.Dev)

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("load embedded frontend: %w", err)
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

	// Open the metadata store; it reconciles any session left "running" by a
	// previous process to "stopped" on open (§8).
	store, err := storage.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	log.Info("store ready", "db", cfg.DB)

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
