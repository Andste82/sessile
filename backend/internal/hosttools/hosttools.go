// Package hosttools reaches a task's host for the agent that runs on the
// sessile server (PROJECT_PLAN.md §4.12, v0.9): files over SFTP, commands
// over exec channels, both on one SSH connection dialled with the host's
// pinned key.
//
// It is the machinery under the agent's tools, not a surface of its own: the
// HTTP API still exposes only internal/hostops' typed operations. What makes
// a command here acceptable where one in the API would not be is the trust
// boundary the plan already uses for SSH paths — the target is the user's own
// host, reached with their own credentials, in a task they own.
package hosttools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// Limits on what one call may move, so a runaway command or a huge file
// cannot fill the agent's context or the server's memory.
const (
	MaxRead     = 2 << 20 // bytes read from one file
	MaxOutput   = 64 << 10
	MaxLines    = 2000
	MaxMatches  = 200
	DefaultRun  = 2 * time.Minute
	MaxRunLimit = 60 * time.Minute
)

// Client is one task's connection to its host.
type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	// windows records the host's declared OS, which decides how a command is
	// spelled. Never guessed from the connection.
	windows bool
}

// Dial opens the connection. The target carries the pinned fingerprint, so an
// unknown or changed host key fails here exactly as it does for a session.
func Dial(target sshpty.Target) (*Client, error) {
	client, err := sshpty.Dial(target)
	if err != nil {
		return nil, err
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("open sftp: %w", err)
	}
	return &Client{ssh: client, sftp: sc, windows: target.TargetOS == "windows"}, nil
}

// Close ends the connection and everything on it.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.sftp != nil {
		c.sftp.Close()
	}
	return c.ssh.Close()
}

// Alive reports whether the connection still answers. A task's client is
// cached across starts, and a host reboot must not leave a dead one in place.
func (c *Client) Alive() bool {
	if c == nil {
		return false
	}
	_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// SSH exposes the connection for callers that need a channel of their own
// (the MCP tunnel's listener, a PTY for a shell pane).
func (c *Client) SSH() *ssh.Client { return c.ssh }

// SFTP exposes the file client for the same reason.
func (c *Client) SFTP() *sftp.Client { return c.sftp }

// Windows reports the host's declared OS.
func (c *Client) Windows() bool { return c.windows }

// --- files ----------------------------------------------------------------

// ReadFile returns a file's contents with line numbers, from offset (0-based)
// for at most limit lines — the shape agents already expect from their own
// read tool, so the output reads the same wherever the file lives.
func (c *Client) ReadFile(p string, offset, limit int) (string, error) {
	f, err := c.sftp.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxRead))
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "(empty file)\n", nil
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 || limit > MaxLines {
		limit = MaxLines
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := offset; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n... %d more lines; read again with offset=%d\n", len(lines)-end, end)
	}
	return b.String(), nil
}

// WriteFile creates or overwrites a file, making its directory first.
func (c *Client) WriteFile(p, content string) error {
	if dir := path.Dir(p); dir != "" && dir != "." {
		_ = c.sftp.MkdirAll(dir)
	}
	f, err := c.sftp.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte(content))
	return err
}

// ErrNotUnique is an edit whose old text appears more than once.
type ErrNotUnique struct {
	Path  string
	Count int
}

func (e *ErrNotUnique) Error() string {
	return fmt.Sprintf("that text appears %d times in %s — include more context to make it unique, or set replace_all", e.Count, e.Path)
}

// ErrNoMatch is an edit whose old text is not in the file.
var ErrNoMatch = errors.New("that text is not in the file")

// EditFile replaces an exact string. Unique by default, because a silent
// second replacement is how an agent corrupts a file it cannot see.
func (c *Client) EditFile(p, old, replacement string, all bool) (int, error) {
	f, err := c.sftp.Open(p)
	if err != nil {
		return 0, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxRead))
	f.Close()
	if err != nil {
		return 0, err
	}
	body := string(raw)
	n := strings.Count(body, old)
	switch {
	case old == "":
		return 0, errors.New("old_string is required")
	case n == 0:
		return 0, ErrNoMatch
	case n > 1 && !all:
		return 0, &ErrNotUnique{Path: path.Base(p), Count: n}
	}
	if all {
		body = strings.ReplaceAll(body, old, replacement)
	} else {
		body = strings.Replace(body, old, replacement, 1)
	}
	return n, c.WriteFile(p, body)
}

// Entry is one directory entry.
type Entry struct {
	Name  string
	IsDir bool
	Size  int64
}

// ListDir lists one directory, directories first, then by name.
func (c *Client) ListDir(p string) ([]Entry, error) {
	infos, err := c.sftp.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, Entry{Name: fi.Name(), IsDir: fi.IsDir(), Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Stat reports on one path.
func (c *Client) Stat(p string) (Entry, error) {
	fi, err := c.sftp.Stat(p)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: path.Base(p), IsDir: fi.IsDir(), Size: fi.Size()}, nil
}

// MkdirAll makes a directory and its parents.
func (c *Client) MkdirAll(p string) error { return c.sftp.MkdirAll(p) }

// Remove deletes one file.
func (c *Client) Remove(p string) error { return c.sftp.Remove(p) }

// Glob lists file names under root matching a shell pattern. The search runs
// on the host — one command instead of walking a tree over the wire.
func (c *Client) Glob(ctx context.Context, root, pattern string) (string, error) {
	if c.windows {
		ps := fmt.Sprintf(`Get-ChildItem -Path %s -Recurse -File -Filter %s | Select-Object -First %d -ExpandProperty FullName`,
			psQuote(root), psQuote(path.Base(pattern)), MaxMatches)
		res, err := c.Run(ctx, RunRequest{Command: ps, Timeout: 30 * time.Second})
		return res.Output, err
	}
	cmd := fmt.Sprintf(`cd %s && find . \( -name .git -o -name node_modules \) -prune -o -type f -name %s -print | sed 's|^\./||' | head -%d`,
		shQuote(root), shQuote(path.Base(pattern)), MaxMatches)
	res, err := c.Run(ctx, RunRequest{Command: cmd, Timeout: 60 * time.Second})
	return res.Output, err
}

// Grep searches file contents under root, on the host, with ripgrep where it
// exists and grep otherwise.
func (c *Client) Grep(ctx context.Context, root, pattern, include string) (string, error) {
	if c.windows {
		filter := "*"
		if include != "" {
			filter = include
		}
		ps := fmt.Sprintf(`Get-ChildItem -Path %s -Recurse -File -Filter %s | Select-String -Pattern %s | Select-Object -First %d | ForEach-Object { "$($_.Path):$($_.LineNumber):$($_.Line)" }`,
			psQuote(root), psQuote(filter), psQuote(pattern), MaxMatches)
		res, err := c.Run(ctx, RunRequest{Command: ps, Timeout: 60 * time.Second})
		return res.Output, err
	}
	inc := ""
	rgInc := ""
	if include != "" {
		inc = " --include=" + shQuote(include)
		rgInc = " --glob " + shQuote(include)
	}
	cmd := fmt.Sprintf(`cd %s && if command -v rg >/dev/null 2>&1; then rg --line-number --no-heading --max-count 10%s -e %s . | head -%d; else grep -rn --exclude-dir=.git --exclude-dir=node_modules%s -e %s . | head -%d; fi`,
		shQuote(root), rgInc, shQuote(pattern), MaxMatches, inc, shQuote(pattern), MaxMatches)
	res, err := c.Run(ctx, RunRequest{Command: cmd, Timeout: 120 * time.Second})
	return res.Output, err
}

// --- commands --------------------------------------------------------------

// RunRequest is one command on the host.
type RunRequest struct {
	Command string
	Dir     string
	Timeout time.Duration
	// Env is set for this command only — the Git credential helper, and
	// nothing that outlives the call (§4.12.9).
	Env [][2]string
	// Container runs the command inside the task's devcontainer instead of on
	// the host, through the devcontainer CLI that is already there.
	Container bool
	// Workspace is the devcontainer's workspace folder, needed when Container
	// is set.
	Workspace string
	// OnOutput, when set, receives output as it arrives, for the task panel.
	OnOutput func([]byte)
}

// RunResult is what a command left behind.
type RunResult struct {
	Output    string
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

// Run executes one command and waits for it, bounded by its timeout. Output is
// streamed to OnOutput as it arrives and returned tail-first-truncated, so a
// chatty build cannot flood the agent's context.
func (c *Client) Run(ctx context.Context, r RunRequest) (RunResult, error) {
	if strings.TrimSpace(r.Command) == "" {
		return RunResult{}, errors.New("command is required")
	}
	if r.Timeout <= 0 {
		r.Timeout = DefaultRun
	}
	if r.Timeout > MaxRunLimit {
		r.Timeout = MaxRunLimit
	}
	session, err := c.ssh.NewSession()
	if err != nil {
		return RunResult{}, fmt.Errorf("open a command channel: %w", err)
	}
	defer session.Close()

	w := &boundedWriter{limit: MaxOutput, onWrite: r.OnOutput}
	session.Stdout, session.Stderr = w, w
	if err := session.Start(c.command(r)); err != nil {
		return RunResult{}, fmt.Errorf("start the command: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	timer := time.NewTimer(r.Timeout)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done
		return RunResult{Output: w.String(), TimedOut: true, Truncated: w.truncated, ExitCode: -1},
			fmt.Errorf("the command was still running after %s and was stopped", r.Timeout)
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done
		return RunResult{Output: w.String(), Truncated: w.truncated, ExitCode: -1}, ctx.Err()
	}

	res := RunResult{Output: w.String(), Truncated: w.truncated}
	if waitErr != nil {
		var exit *ssh.ExitError
		if errors.As(waitErr, &exit) {
			res.ExitCode = exit.ExitStatus()
			return res, nil
		}
		return res, waitErr
	}
	return res, nil
}

// command spells the request for the host's shell: the working directory, the
// per-call environment, and the devcontainer wrapper when asked for.
func (c *Client) command(r RunRequest) string {
	if c.windows {
		var b strings.Builder
		if r.Dir != "" {
			fmt.Fprintf(&b, "Set-Location %s; ", psQuote(r.Dir))
		}
		for _, kv := range r.Env {
			fmt.Fprintf(&b, "$env:%s=%s; ", kv[0], psQuote(kv[1]))
		}
		b.WriteString(r.Command)
		return fmt.Sprintf(`powershell -NoProfile -NonInteractive -Command %s`, psQuote(b.String()))
	}
	inner := r.Command
	if r.Container {
		// The container is used, never managed: one devcontainer exec, with
		// the command handed to the container's own shell (§4.12.3).
		inner = fmt.Sprintf("devcontainer exec --workspace-folder %s sh -lc %s",
			shQuote(r.Workspace), shQuote(r.Command))
	}
	var b strings.Builder
	if r.Dir != "" {
		fmt.Fprintf(&b, "cd %s && ", shQuote(r.Dir))
	}
	for _, kv := range r.Env {
		fmt.Fprintf(&b, "%s=%s ", kv[0], shQuote(kv[1]))
	}
	if len(r.Env) > 0 {
		b.WriteString("env ")
	}
	b.WriteString(inner)
	return fmt.Sprintf("sh -lc %s", shQuote(b.String()))
}

// boundedWriter keeps the last limit bytes and forwards everything live.
type boundedWriter struct {
	limit     int
	buf       []byte
	truncated bool
	onWrite   func([]byte)
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite(p)
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-w.limit:]...)
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedWriter) String() string { return string(w.buf) }

// shQuote is single-quoting for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psQuote is single-quoting for PowerShell.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
