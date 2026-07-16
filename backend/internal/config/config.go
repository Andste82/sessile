// Package config holds runtime configuration parsed from CLI flags with
// environment-variable fallbacks (see PROJECT_PLAN.md §9).
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is the application version, injected at build time via ldflags. The
// literal is a placeholder on purpose — releases derive the real value from the
// git tag, so hardcoding a number here would only ever be stale.
var Version = "dev"

// Config is the fully-resolved application configuration.
type Config struct {
	Root        string   // sandbox root; all sessions run inside this tree
	Addr        string   // listen address, e.g. ":8080"
	DB          string   // path to the SQLite database file
	Shells      []string // shell allowlist
	BufferSize  int      // per-session ring buffer size in bytes
	LogLevel    string   // slog level: debug|info|warn|error
	Dev         bool     // dev mode: relaxes WS origin check for the Vite proxy
	AllowOrigin string   // extra allowed WS origin (e.g. http://localhost:5173)
}

// Parse builds a Config from the given argument list (excluding the program
// name). Flags fall back to environment variables, then to defaults.
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)

	root := fs.String("root", env("TSM_ROOT", ""), "sandbox root directory (required)")
	addr := fs.String("addr", env("TSM_ADDR", ":8080"), "listen address")
	db := fs.String("db", env("TSM_DB", ""), "SQLite database path (default <root>/.tsm/sessions.db)")
	shells := fs.String("shells", env("TSM_SHELLS", "bash,zsh,fish"), "comma-separated shell allowlist")
	bufferSize := fs.String("buffer-size", env("TSM_BUFFER_SIZE", "524288"), "per-session ring buffer size in bytes")
	logLevel := fs.String("log-level", env("TSM_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	dev := fs.Bool("dev", envBool("TSM_DEV", false), "dev mode (relaxes WS origin check)")
	allowOrigin := fs.String("allow-origin", env("TSM_ALLOW_ORIGIN", ""), "additional allowed WebSocket origin")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if *root == "" {
		return nil, fmt.Errorf("--root is required")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if fi, err := os.Stat(absRoot); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("root %q is not an existing directory", absRoot)
	}

	dbPath := *db
	if dbPath == "" {
		dbPath = filepath.Join(absRoot, ".tsm", "sessions.db")
	}

	bufSize, err := strconv.Atoi(*bufferSize)
	if err != nil || bufSize <= 0 {
		return nil, fmt.Errorf("invalid buffer-size %q", *bufferSize)
	}

	var shellList []string
	for _, s := range strings.Split(*shells, ",") {
		if s = strings.TrimSpace(s); s != "" {
			shellList = append(shellList, s)
		}
	}
	if len(shellList) == 0 {
		return nil, fmt.Errorf("shell allowlist is empty")
	}

	if *dev && *allowOrigin == "" {
		*allowOrigin = "http://localhost:5173"
	}

	return &Config{
		Root:        absRoot,
		Addr:        *addr,
		DB:          dbPath,
		Shells:      shellList,
		BufferSize:  bufSize,
		LogLevel:    *logLevel,
		Dev:         *dev,
		AllowOrigin: *allowOrigin,
	}, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}
