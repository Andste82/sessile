package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Andste82/sessile/backend/internal/envfile"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// EnvVar is one environment variable of a session (issue #123). Secret
// values are write-only: they live in Info and the store but never leave
// through ToJSON, events, logs or error messages.
type EnvVar = envfile.Entry

// CreateOptions carries the environment a new session starts with.
type CreateOptions struct {
	Env    []EnvVar // already merged with the user's defaults (envfile.Inherit)
	DotEnv bool     // read ".env" at every spawn
}

// EnvPatchEntry is one row of a full-replacement environment edit.
type EnvPatchEntry = envfile.PatchEntry

// EnvUpdate describes the environment part of Update. A nil field is left
// unchanged; a non-nil Entries replaces the whole list (envfile.ApplyPatch).
type EnvUpdate struct {
	Entries *[]EnvPatchEntry
	DotEnv  *bool
}

var (
	// ErrInvalidEnv wraps envfile.ErrInvalid. Its message names entries only,
	// never values.
	ErrInvalidEnv = fmt.Errorf("%w", envfile.ErrInvalid)
	// ErrEnvUnsupported is returned when environment variables or a .env are
	// requested for an SSH host that cannot take them (Windows targets).
	ErrEnvUnsupported = errors.New("host does not support environment variables")
	// ErrEnvRunning is returned when the environment of a session that is not
	// stopped is edited: it only takes effect at the next spawn.
	ErrEnvRunning = errors.New("environment can only be changed while the session is stopped")
)

// invalidEnvError keeps the (value-free) envfile message while matching both
// ErrInvalidEnv and envfile.ErrInvalid.
type invalidEnvError struct{ err error }

func (e *invalidEnvError) Error() string        { return e.err.Error() }
func (e *invalidEnvError) Unwrap() error        { return e.err }
func (e *invalidEnvError) Is(target error) bool { return target == ErrInvalidEnv }

func invalidEnv(err error) error { return &invalidEnvError{err} }

// cloneEnv deep-copies env and sorts it by name. nil stays nil.
func cloneEnv(env []EnvVar) []EnvVar {
	if env == nil {
		return nil
	}
	out := make([]EnvVar, len(env))
	copy(out, env)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toAssignments renders entries as "K=V" strings for exec.Cmd.Env.
func toAssignments(env []EnvVar) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

// checkEnvTarget refuses an environment for an SSH target that cannot apply
// it. Used by create and, against the freshly resolved target, by Restart.
func checkEnvTarget(t sshpty.Target, env []EnvVar, dotenv bool) error {
	if (len(env) > 0 || dotenv) && !sshpty.SupportsEnv(t.TargetOS, t.TerminalType) {
		return ErrEnvUnsupported
	}
	return nil
}

const (
	dotenvName           = ".env"
	warnDotenvMissing    = "no .env file found"
	warnDotenvUnreadable = "cannot read .env"
	warnDotenvTooLarge   = ".env too large, skipped"
)

// readLocalDotenv reads the .env of a local session's start directory.
// The directory passes resolveDir (workspace validation, §4.5) and so does
// the candidate file, including a symlink re-check: a .env that links
// outside the workspace is treated as unreadable and never opened. Every
// failure is a warning; none ever includes file content.
func readLocalDotenv(root, dir string) ([]envfile.Entry, []string) {
	resolvedDir, err := resolveDir(root, dir)
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	candidate := filepath.Join(resolvedDir, dotenvName)

	fi, err := os.Lstat(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{warnDotenvMissing}
		}
		return nil, []string{warnDotenvUnreadable}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, []string{warnDotenvMissing}
			}
			return nil, []string{warnDotenvUnreadable}
		}
		if target != rootResolved && !strings.HasPrefix(target, rootResolved+string(os.PathSeparator)) {
			return nil, []string{warnDotenvUnreadable}
		}
		candidate = target
		if fi, err = os.Stat(candidate); err != nil {
			return nil, []string{warnDotenvUnreadable}
		}
	}
	if !fi.Mode().IsRegular() {
		return nil, []string{warnDotenvUnreadable}
	}

	f, err := os.Open(candidate)
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	defer f.Close()
	// One byte over the cap tells "exactly at the cap" from "larger".
	data, err := io.ReadAll(io.LimitReader(f, envfile.MaxFileBytes+1))
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	if len(data) > envfile.MaxFileBytes {
		return nil, []string{warnDotenvTooLarge}
	}
	return envfile.Parse(data)
}
