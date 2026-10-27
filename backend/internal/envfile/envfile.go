// Package envfile holds everything about a session's user-supplied
// environment variables that is independent of where they are applied:
// the entry type, the limits and name rules, validation, the .env parser,
// and the precedence/merge helpers (issue #123).
//
// Nothing in here ever puts an entry VALUE into an error or warning —
// values may be secrets. Messages name entries and .env line numbers only.
package envfile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Limits. This package is the single source; every other layer imports them.
const (
	MaxEntries     = 100    // entries per session / defaults file / .env
	MaxNameLen     = 128    // bytes
	MaxValueLen    = 4096   // bytes
	MaxTotalBytes  = 16384  // sum of len(name)+len(value) over persisted entries
	MaxFileBytes   = 65536  // .env read cap
	MaxDotenvBytes = 32768  // sum of accepted .env name+value bytes
	MaxExportBytes = 131072 // fully quoted sh -c command line built by sshpty
)

// Entry is one environment variable.
type Entry struct {
	Name  string
	Value string
	// Secret marks a write-only value: it is never returned by the API.
	Secret bool
	// Default means the entry was inherited from the user's defaults at
	// create time and is subordinate to values from a .env file.
	Default bool
}

// ReservedNames are variables the application owns: the terminal identity,
// the locale it forces, and the per-session history variables. They are
// rejected in API input and skipped (with a warning) when read from .env,
// uniformly for local and SSH targets.
var ReservedNames = []string{
	"TERM", "LANG",
	"HISTFILE", "HISTSIZE", "HISTFILESIZE", "SAVEHIST", "PROMPT_COMMAND",
	"fish_history",
}

// IsReserved reports whether name is in ReservedNames (case-sensitive).
func IsReserved(name string) bool {
	for _, r := range ReservedNames {
		if r == name {
			return true
		}
	}
	return false
}

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("invalid environment")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// validName implements ^[A-Za-z_][A-Za-z0-9_]*$ without regexp.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Validate checks names, reserved names, duplicates, NUL bytes and every
// size limit. The error names entries (by name or position), never values.
func Validate(entries []Entry) error {
	if len(entries) > MaxEntries {
		return invalidf("too many entries (max %d)", MaxEntries)
	}
	seen := make(map[string]struct{}, len(entries))
	total := 0
	for i, e := range entries {
		if len(e.Name) > MaxNameLen {
			return invalidf("entry %d: name too long (max %d bytes)", i+1, MaxNameLen)
		}
		if !validName(e.Name) {
			// The offending text is not echoed: it is not known to be a name.
			return invalidf("entry %d: invalid name (letters, digits and _, not starting with a digit)", i+1)
		}
		if IsReserved(e.Name) {
			return invalidf("%s is reserved", e.Name)
		}
		if _, dup := seen[e.Name]; dup {
			return invalidf("%s is defined more than once", e.Name)
		}
		seen[e.Name] = struct{}{}
		if strings.IndexByte(e.Value, 0) >= 0 {
			return invalidf("%s: value must not contain NUL", e.Name)
		}
		if len(e.Value) > MaxValueLen {
			return invalidf("%s: value too long (max %d bytes)", e.Name, MaxValueLen)
		}
		total += len(e.Name) + len(e.Value)
	}
	if total > MaxTotalBytes {
		return invalidf("environment too large (max %d bytes in total)", MaxTotalBytes)
	}
	if ExportSize(entries) > MaxExportBytes {
		return invalidf("environment too large once quoted for the host command line (max %d bytes)", MaxExportBytes)
	}
	return nil
}

// ExportSize is the size of the `sh -c '<export K='v'; ...>'` argument that
// sshpty builds for entries: every value is single-quoted, and the whole
// preamble is single-quoted once more, so each literal ' in a value costs 13
// bytes in the worst case. It lets create and edit refuse an environment the
// spawn would reject later (sshpty.ErrEnvTooLarge), for the session's own
// entries alone; a .env added at spawn time is still checked there.
func ExportSize(entries []Entry) int {
	n, quotes := 0, 0
	for _, e := range entries {
		c := strings.Count(e.Value, "'")
		// "export " NAME "=" '<value>' "; " with each ' in value as '\''.
		n += len("export ") + len(e.Name) + 1 + len(e.Value) + 2 + 3*c + len("; ")
		quotes += 2 + 3*c
	}
	// Outer quoting: two quote characters plus three extra bytes per inner one.
	return n + 2 + 3*quotes
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
}

// Merge computes the environment a spawn applies: default-flagged session
// entries, overlaid by .env entries, overlaid by the non-default session
// entries. The result is sorted by name; the winner's flags are retained
// (.env winners carry Secret=false). Pure; the inputs are not modified.
func Merge(session, dotenv []Entry) []Entry {
	m := make(map[string]Entry, len(session)+len(dotenv))
	for _, e := range session {
		if e.Default {
			m[e.Name] = e
		}
	}
	for _, e := range dotenv {
		e.Secret, e.Default = false, false
		m[e.Name] = e
	}
	for _, e := range session {
		if !e.Default {
			m[e.Name] = e
		}
	}
	out := make([]Entry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sortEntries(out)
	return out
}

// Inherit builds the entry list stored on a new session: user defaults not
// named in removed and not overridden by an own entry become copies flagged
// Default; own entries are kept with Default=false. Sorted by name.
func Inherit(defaults, own []Entry, removed []string) []Entry {
	skip := make(map[string]struct{}, len(removed)+len(own))
	for _, n := range removed {
		skip[n] = struct{}{}
	}
	for _, e := range own {
		skip[e.Name] = struct{}{}
	}
	out := make([]Entry, 0, len(defaults)+len(own))
	for _, d := range defaults {
		if _, ok := skip[d.Name]; ok {
			continue
		}
		d.Default = true
		out = append(out, d)
	}
	for _, e := range own {
		e.Default = false
		out = append(out, e)
	}
	sortEntries(out)
	return out
}

// PatchEntry is one row of a full-replacement edit. Value == nil means "keep
// the stored value" and is only meaningful for a secret entry.
type PatchEntry struct {
	Name   string
	Value  *string
	Secret bool
}

// ApplyPatch replaces existing with patch (names absent from patch are
// deleted) and validates the result.
func ApplyPatch(existing []Entry, patch []PatchEntry) ([]Entry, error) {
	byName := make(map[string]Entry, len(existing))
	for _, e := range existing {
		byName[e.Name] = e
	}
	out := make([]Entry, 0, len(patch))
	for _, p := range patch {
		old, had := byName[p.Name]
		if p.Value != nil {
			e := Entry{Name: p.Name, Value: *p.Value, Secret: p.Secret}
			if had && old.Default && !old.Secret && !p.Secret && old.Value == *p.Value {
				e.Default = true
			}
			out = append(out, e)
			continue
		}
		switch {
		case !p.Secret:
			return nil, invalidf("%s: a value is required", p.Name)
		case !had:
			return nil, invalidf("%s: a value is required for a new entry", p.Name)
		}
		// Existing entry kept (or made secret) with its stored value.
		old.Secret = true
		out = append(out, old)
	}
	if err := Validate(out); err != nil {
		return nil, err
	}
	sortEntries(out)
	return out, nil
}
