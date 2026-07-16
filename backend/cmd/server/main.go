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
	"syscall"
	"time"

	"github.com/Andste82/sessile/backend/internal/api"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/ws"
	"github.com/Andste82/sessile/backend/web"
)

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
		"version", config.Version, "root", cfg.Root, "addr", cfg.Addr, "dev", cfg.Dev)

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("load embedded frontend: %w", err)
	}

	// Open the metadata store; it reconciles any session left "running" by a
	// previous process to "stopped" on open (§8).
	store, err := storage.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	log.Info("store ready", "db", cfg.DB)

	manager := session.NewManager(cfg.Root, cfg.Shells, cfg.BufferSize, store, log)
	wsHandler := ws.NewHandler(manager, cfg, log)

	srv := api.NewServer(cfg, manager, wsHandler, log)
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
