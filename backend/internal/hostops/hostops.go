// Package hostops implements a session's process tree, file browser, and
// file transfer (PROJECT_PLAN.md §4.10) as a small, fixed set of named
// operations — never a generic remote-command or file-transfer surface
// (CLAUDE.md's "Scope" rule).
//
// Two independent axes compose rather than multiply: Transport (local vs.
// the session's own already-dialed SSH connection) and Platform (Linux vs.
// Windows, needed only for process listing — file operations are
// OS-agnostic via the SFTP subsystem).
package hostops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrUnsupportedPlatform is returned by HostSession.ProcessTree when the
// target's Platform has no ProcessTree support (§4.10 — Windows before
// M23's windowsPlatform lands for a given target, or an "other" OS).
var ErrUnsupportedPlatform = errors.New("hostops: unsupported platform")

// Transport moves bytes/commands to and from a session's target.
// localTransport and sshTransport are its only two implementations.
type Transport interface {
	// Exec runs line as a single command in the target's own shell and
	// reports what it printed. line is built exclusively by this package's
	// own callers (Platform implementations, §4.10's "Exec stays internal")
	// from a fixed template plus quoted arguments — never supplied by a
	// caller outside it.
	Exec(ctx context.Context, line string) (Result, error)

	// Files returns the target's file operations. Safe to call repeatedly;
	// implementations that need a session of their own (SSH) open it lazily
	// and reuse it.
	Files() FileTransport
}

// Result is one command's output. A non-zero ExitCode is not itself an
// error — the command ran and reported failure, which is a different case
// from failing to run at all (no session, a lost connection, a cancelled
// context).
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// FileTransport is deliberately OS-agnostic: local's is backed by stdlib
// os.*, SSH's by github.com/pkg/sftp — the sftp-server subsystem
// OpenSSH-for-Windows serves identically to Linux, so this one interface
// and its one SSH implementation cover every SSH target OS with no
// per-platform branch.
type FileTransport interface {
	// Resolve returns path in its canonical absolute form — for SSH, the
	// target's own filesystem root ("/", not this app's concept of one);
	// for local, the validated absolute host path §4.5 already resolved
	// to. Used so the file browser can show and navigate real absolute
	// paths for an SSH target (§4.10) rather than a synthetic starting
	// point with no way above it.
	Resolve(ctx context.Context, path string) (string, error)
	// Stat returns one entry's own metadata — used ahead of Copy/Delete to
	// report a total before the first byte moves (§5.2), and by List's
	// implementations internally for each entry.
	Stat(ctx context.Context, path string) (DirEntry, error)
	List(ctx context.Context, path string) ([]DirEntry, error)
	Read(ctx context.Context, path string) ([]byte, error)
	// Open streams a file's content instead of buffering it whole, as Read
	// does — for the one caller (the download handler) that must not hold
	// an entire file in memory just to relay it to an HTTP response writer.
	// The caller must Close the returned reader.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Write(ctx context.Context, path string, data []byte) error
	// Create opens path for writing, truncating it if it exists — the
	// upload handler's streaming counterpart to Write, used to write into
	// a ".part" staging path so a failed or aborted upload never leaves a
	// partially-written file at the real destination (committed atomically
	// via Commit once the whole body has been written and closed without
	// error). The caller must Close the returned writer and check its
	// error — a full destination surfaces there (ENOSPC/EDQUOT), not from
	// the write calls that filled the buffer before it.
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	// Commit renames oldpath to newpath, overwriting newpath if it already
	// exists — deliberately not Rename, which is the user-facing "move"
	// and must keep refusing to clobber. Used only to commit a Create'd
	// ".part" staging file over its real destination.
	Commit(ctx context.Context, oldpath, newpath string) error
	Rename(ctx context.Context, oldpath, newpath string) error // Move
	// Remove deletes a file, or a directory and everything under it. Neither
	// SFTP nor a plain syscall has a recursive-delete primitive, so this
	// walks and deletes bottom-up.
	Remove(ctx context.Context, path string) error
	// Copy reads src fully and writes it to dst. Whole file in memory —
	// fine at the sizes this feature targets (§4.10); revisit if it grows
	// into bulk transfer.
	Copy(ctx context.Context, src, dst string) error
}

// DirEntry is one entry from FileTransport.List.
type DirEntry struct {
	Name  string
	IsDir bool
	// IsRegular is required to trust Size as a byte count a read will
	// actually produce — a special file (a device, a FIFO) can report any
	// Size at all with no relation to what reading it does. The download
	// handler uses this to decide whether Size is safe to publish as an
	// HTTP Content-Length.
	IsRegular bool
	Size      int64
	ModTime   time.Time
}

// Platform is the one thing about a target that is genuinely OS-shaped:
// process listing. File operations do not need this — FileTransport is
// OS-agnostic by construction — so a target with no Platform support still
// gets the full file browser and transfer; only ProcessTree is gated.
type Platform interface {
	// ProcessTree returns rootPID's descendants, assembled into a tree —
	// not rootPID itself, which the caller already knows (its session's own
	// foreground PID, §4.7) — or, when rootPID is nil, the whole target's
	// process forest: every process with no visible parent in the listing,
	// each its own root. nil exists because there is no portable "root
	// pid" to hand implementations instead — Linux's "1" convention (init)
	// has no Windows equivalent, and guessing one risks a self-referential
	// entry a caller would have no way to know is wrong. Implementations
	// run one fixed, hardcoded listing command (never a caller-supplied
	// one, §4.10) over t.Exec.
	ProcessTree(ctx context.Context, t Transport, rootPID *int) ([]Process, error)
}

// Process is one entry in a process tree.
type Process struct {
	PID, PPID int
	Command   string
	Children  []Process // pre-assembled; callers never re-link a flat list
}

// HostSession is the top-level handle a session's hostops hang off — one
// built per session, alongside its Backend (§4.2), composing a Transport
// (how to reach the target) with a Platform (what the target looks like,
// consulted only by ProcessTree).
type HostSession struct {
	transport Transport
	platform  Platform // nil means "no ProcessTree support for this target"
}

// NewHostSession composes transport and platform into a HostSession.
// platform may be nil — see ProcessTree.
func NewHostSession(transport Transport, platform Platform) *HostSession {
	return &HostSession{transport: transport, platform: platform}
}

// ProcessTree returns rootPID's descendants for this session's target — or,
// rootPID nil, the whole target's process forest — or ErrUnsupportedPlatform
// if it has no Platform (a target OS with no ProcessTree implementation
// yet, or "other"/unset).
func (h *HostSession) ProcessTree(ctx context.Context, rootPID *int) ([]Process, error) {
	if h.platform == nil {
		return nil, ErrUnsupportedPlatform
	}
	return h.platform.ProcessTree(ctx, h.transport, rootPID)
}

// Cwd reports the working directory of pid on this session's target, as an
// absolute path. The caller supplies the pid because only it knows which one
// is meant: a local session's shell pid comes from the kernel (§4.7), an SSH
// session's from SessionRootPID.
//
// One fixed command over the session's own Transport, so local and SSH share
// an implementation — Exec stays internal to this package (§4.10), and this
// is exactly the kind of specific, named question it exists to answer.
//
// ok is false rather than a guess when the target can't answer: a Windows
// target has no /proc, and a pid that has exited has no cwd. Callers treat
// that as "unknown", never as "the root".
func (h *HostSession) Cwd(ctx context.Context, pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	res, err := h.transport.Exec(ctx, fmt.Sprintf("readlink /proc/%d/cwd", pid))
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	cwd := strings.TrimSpace(string(res.Stdout))
	if cwd == "" || !strings.HasPrefix(cwd, "/") {
		return "", false
	}
	return cwd, true
}

// Files returns this session's target's file operations (§4.10 M24+).
func (h *HostSession) Files() FileTransport { return h.transport.Files() }

// SessionAware is optionally implemented by a Transport that can identify
// its own session's identity on the target — which process it is, and
// what's currently in its foreground. Local doesn't implement it: the
// caller already has both facts straight from the kernel (§4.7), no round
// trip needed, so HostSession's methods below just report "unknown" for
// it, the same answer a failed lookup would give. sshTransport implements
// it via /proc reads once it knows its own PID (§4.10). A future
// transport with a more direct source of truth for the same two questions
// (Docker's exec API hands back its own PID authoritatively, no
// correlation needed at all) can implement this however fits its own
// mechanism — HostSession only checks that the interface is satisfied,
// never which concrete transport is behind it, so nothing here needs to
// change for that to plug in.
type SessionAware interface {
	// SessionRootPID finds this session's own PID on the target, with
	// certainty or not at all — it never guesses (§4.10's design note on
	// why: a plausible-looking wrong answer is worse than none).
	SessionRootPID(ctx context.Context) (pid int, ok bool)
	// Foreground reports the session's current foreground process — name
	// and the chain leading to it (a script, then what it's actually
	// running) — with the same certainty-or-nothing contract.
	Foreground(ctx context.Context) (name string, chain []string, ok bool)
}

// SessionRootPID finds this session's own PID on the target, if its
// Transport can answer that (SessionAware) — "unknown" (ok=false)
// otherwise, never a guess.
func (h *HostSession) SessionRootPID(ctx context.Context) (pid int, ok bool) {
	aware, isAware := h.transport.(SessionAware)
	if !isAware {
		return 0, false
	}
	return aware.SessionRootPID(ctx)
}

// Foreground reports this session's current foreground process, if its
// Transport can answer that (SessionAware) — "unknown" (ok=false)
// otherwise, never a guess.
func (h *HostSession) Foreground(ctx context.Context) (name string, chain []string, ok bool) {
	aware, isAware := h.transport.(SessionAware)
	if !isAware {
		return "", nil, false
	}
	return aware.Foreground(ctx)
}
