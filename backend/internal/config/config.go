// Package config holds runtime configuration parsed from CLI flags with
// environment-variable fallbacks (see PROJECT_PLAN.md §9).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// usageOut is where the flag set writes usage text and parse errors. It is a
// variable so tests can capture it instead of dirtying stderr.
var usageOut io.Writer = os.Stderr

// Version is the application version, injected at build time via ldflags. The
// literal is a placeholder on purpose — releases derive the real value from the
// git tag, so hardcoding a number here would only ever be stale.
var Version = "dev"

// ErrVersionRequested is returned by Parse when --version was given. Like
// flag.ErrHelp it reports intent, not failure: the caller prints the version
// and exits 0.
var ErrVersionRequested = errors.New("version requested")

// Config is the fully-resolved application configuration.
type Config struct {
	Addr    string // listen address, e.g. ":8080"
	DataDir string // directory holding config.yml, users.yml, users/, the
	// database, scrollback snapshots and shell history
	DB string // SQLite database file, always <DataDir>/sessions.db
	// WorkspaceDir is the local-host sandbox root (only reachable when
	// config.yml's allowLocalHost is on). Defaults to <DataDir>/workspace,
	// but is a separate flag/volume so an operator can mount it apart from
	// DataDir — e.g. Docker's default /config + /workspace split, so the
	// small, sensitive state in DataDir (users.yml, hosts.yml with
	// credentials) can be backed up separately from a workspace that may be
	// large and is never sensitive on its own.
	WorkspaceDir string
	Shells       []string // shell allowlist (local-host sessions only)
	BufferSize   int      // per-session ring buffer size in bytes
	// SessionRetention discards stopped sessions idle for longer than this on
	// startup. Zero keeps them forever, which is the default.
	SessionRetention time.Duration
	LogLevel         string // slog level: debug|info|warn|error
	Dev              bool   // dev mode: relaxes WS origin check for the Vite proxy
	AllowOrigin      string // extra allowed WS origin (e.g. http://localhost:5173)
}

// errRemovedDB explains where --db went. The database is no longer separately
// addressable: it is one of the things inside --data-dir.
var errRemovedDB = errors.New(
	"--db was removed: use --data-dir for the directory holding the database, " +
		"scrollback and shell history (the database is always <data-dir>/sessions.db)")

// errRemovedRoot explains where --root went. It is now --workspace-dir: same
// idea (the local-host sandbox directory), renamed because "root" reads as
// the admin account in a multi-user app, and its access is now gated at
// runtime by config.yml's allowLocalHost (PROJECT_PLAN.md §9) rather than
// being the server's only mode.
var errRemovedRoot = errors.New(
	"--root was renamed to --workspace-dir (same meaning: the local-host " +
		"sandbox directory; access to it is now gated by config.yml's allowLocalHost)")

// removedFlag reports whether args mention name (without its leading dashes)
// in any of the spellings Go's flag package would have accepted.
func removedFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false // everything after this is not a flag
		}
		got, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && got == name {
			return true
		}
	}
	return false
}

// Parse builds a Config from the given argument list (excluding the program
// name). Flags fall back to environment variables, then to defaults.
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("sessile", flag.ContinueOnError)
	fs.SetOutput(usageOut)

	addr := fs.String("addr", env("TSM_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data-dir", env("TSM_DATA_DIR", "./data"),
		"directory for server state: config.yml, users.yml, per-user hosts.yml, "+
			"the database, scrollback snapshots and shell history")
	workspaceDir := fs.String("workspace-dir", env("TSM_WORKSPACE_DIR", ""),
		"local-host sandbox directory, only reachable when config.yml's allowLocalHost is on "+
			"(default <data-dir>/workspace)")
	shells := fs.String("shells", env("TSM_SHELLS", "bash,zsh,fish"), "comma-separated shell allowlist")
	bufferSize := fs.String("buffer-size", env("TSM_BUFFER_SIZE", "524288"), "per-session ring buffer size in bytes")
	sessionRetention := fs.String("session-retention", env("TSM_SESSION_RETENTION", "0"),
		"discard stopped sessions idle longer than this on startup, as a Go duration (e.g. 720h); 0 keeps them forever")
	logLevel := fs.String("log-level", env("TSM_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	dev := fs.Bool("dev", envBool("TSM_DEV", false), "dev mode (relaxes WS origin check)")
	allowOrigin := fs.String("allow-origin", env("TSM_ALLOW_ORIGIN", ""), "additional allowed WebSocket origin")
	showVersion := fs.Bool("version", false, "print version and exit")

	// --db named a file and then silently claimed the directory around it for
	// scrollback/ and history/. --root was renamed to --workspace-dir. Both
	// are answered with a helpful error instead of "flag provided but not
	// defined", which tells an operator a flag is gone but not what replaced
	// it.
	if removedFlag(args, "db") {
		return nil, errRemovedDB
	}
	if removedFlag(args, "root") {
		return nil, errRemovedRoot
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Before any validation: --version must work with no other flags, and
	// asking for the version is not a request to start a server.
	if *showVersion {
		return nil, ErrVersionRequested
	}

	// Everything the server keeps lives under one directory it owns: the
	// database, the scrollback snapshots, the per-session shell history,
	// config.yml, users.yml and per-user hosts.yml. It is named directly
	// rather than inferred from a file inside it, which is what --db used to
	// do — pointing that at /var/lib/sessions.db quietly made /var/lib the
	// state directory and put history/ and scrollback/ in it.
	//
	// A relative --data-dir is resolved here, against the server's working
	// directory. It cannot be left relative: this path ends up in a shell's
	// environment as HISTFILE, and that shell runs in the session's
	// directory, where a relative path would point somewhere else entirely.
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data-dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data-dir %q: %w", dir, err)
	}
	dbPath := filepath.Join(dir, "sessions.db")

	// The local-host sandbox is a separate directory on purpose — separate
	// enough to be its own Docker volume — so it can be excluded from a
	// backup of the small, sensitive state in --data-dir (users.yml,
	// hosts.yml with credentials) without a second thought. Left unset, it
	// defaults to a subdirectory of --data-dir, which keeps a bare `go run`
	// or single-volume deployment to one directory.
	workspaceRoot := *workspaceDir
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(dir, "workspace")
	} else {
		workspaceRoot, err = filepath.Abs(workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace-dir: %w", err)
		}
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace-dir %q: %w", workspaceRoot, err)
	}

	bufSize, err := strconv.Atoi(*bufferSize)
	if err != nil || bufSize <= 0 {
		return nil, fmt.Errorf("invalid buffer-size %q", *bufferSize)
	}

	// Retention is off by default and an empty value means the same as zero.
	// Note Go durations have no day unit: a month is 720h, not 30d.
	var retention time.Duration
	if s := strings.TrimSpace(*sessionRetention); s != "" {
		retention, err = time.ParseDuration(s)
		if err != nil || retention < 0 {
			return nil, fmt.Errorf("invalid session-retention %q: want a Go duration like 720h, or 0 to disable", *sessionRetention)
		}
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
		Addr:         *addr,
		DataDir:      dir,
		DB:           dbPath,
		WorkspaceDir: workspaceRoot,
		Shells:       shellList,
		BufferSize:   bufSize,

		SessionRetention: retention,

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
