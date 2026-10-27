package hostops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// This file is the session-less directory lister (PROJECT_PLAN.md §4.10):
// what NewSessionDialog's start-directory chooser needs before any session
// exists. It is deliberately not a FileTransport and not part of
// HostSession — it takes exactly one path argument per call, returns
// directory names only, and never writes. It does not widen the fixed
// operation set; it is the same read-only listing, minus the session.

// ErrBrowseUnavailable means the host's SFTP subsystem could not be opened,
// so nothing can be listed. Not retried silently: the caller reports it.
var ErrBrowseUnavailable = errors.New("the host's SFTP subsystem is unavailable")

// BrowseNotFoundError is a path that does not exist, is not a directory, or
// cannot be read. Path is the path the caller asked for, verbatim.
type BrowseNotFoundError struct {
	Path  string
	Cause error // permission denied etc.; nil for a plain missing path
}

func (e *BrowseNotFoundError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("directory %q cannot be read on host: %v", e.Path, e.Cause)
	}
	return fmt.Sprintf("directory %q not found on host", e.Path)
}

// BrowseResult is one directory listing: the resolved absolute path, its
// parent ("" at the filesystem root) and the names of its sub-directories.
type BrowseResult struct {
	Path        string
	Parent      string
	Directories []string
}

// DirBrowser lists directories over one SFTP client opened on a caller-owned,
// already TOFU-verified *ssh.Client. Safe for concurrent use.
type DirBrowser struct {
	client *sftp.Client
}

// NewDirBrowser opens the SFTP subsystem on c. The caller keeps ownership of
// c; Close only releases the SFTP channel.
func NewDirBrowser(c *ssh.Client) (*DirBrowser, error) {
	sc, err := sftp.NewClient(c)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBrowseUnavailable, err)
	}
	return &DirBrowser{client: sc}, nil
}

// sftpOpenTimeout bounds the SFTP subsystem handshake.
const sftpOpenTimeout = 15 * time.Second

// NewDirBrowserContext is NewDirBrowser with a deadline: sftp.NewClient
// ignores contexts and blocks on a host that accepts the SSH session but
// never answers the subsystem. On ctx cancellation or timeout the ssh client
// is closed (which unblocks the handshake) and an error is returned; on
// failure the caller must not reuse c.
func NewDirBrowserContext(ctx context.Context, c *ssh.Client) (*DirBrowser, error) {
	type result struct {
		b   *DirBrowser
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := NewDirBrowser(c)
		ch <- result{b, err}
	}()
	t := time.NewTimer(sftpOpenTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.b, r.err
	case <-ctx.Done():
		_ = c.Close()
		return nil, ctx.Err()
	case <-t.C:
		_ = c.Close()
		return nil, fmt.Errorf("%w: sftp subsystem did not answer in time", ErrBrowseUnavailable)
	}
}

// Close releases the SFTP channel. It unblocks any in-flight call.
func (b *DirBrowser) Close() error { return b.client.Close() }

// Browse resolves p ("" or "." means the home directory, anything else must
// be absolute — the caller validates) and lists its sub-directories, sorted
// case-insensitively. Symlinks that point at a directory are included.
func (b *DirBrowser) Browse(p string) (BrowseResult, error) {
	req := p
	target := p
	if target == "" {
		target = "."
	}
	abs, err := b.client.RealPath(target)
	if err != nil {
		return BrowseResult{}, classifyBrowseErr(req, err)
	}
	if req == "" || req == "." {
		req = abs
	}
	info, err := b.client.Stat(abs)
	if err != nil {
		return BrowseResult{}, classifyBrowseErr(req, err)
	}
	if !info.IsDir() {
		return BrowseResult{}, &BrowseNotFoundError{Path: req}
	}
	entries, err := b.client.ReadDir(abs)
	if err != nil {
		return BrowseResult{}, classifyBrowseErr(req, err)
	}

	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.IsDir():
			dirs = append(dirs, e.Name())
		case e.Mode()&os.ModeSymlink != 0:
			// ReadDir reports the link itself; follow it. A broken link
			// or a link to a file is simply not a directory.
			if t, err := b.client.Stat(path.Join(abs, e.Name())); err == nil && t.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		a, c := strings.ToLower(dirs[i]), strings.ToLower(dirs[j])
		if a != c {
			return a < c
		}
		return dirs[i] < dirs[j]
	})

	res := BrowseResult{Path: abs, Directories: dirs}
	if abs != "/" {
		res.Parent = path.Dir(abs)
	}
	return res, nil
}

func classifyBrowseErr(req string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &BrowseNotFoundError{Path: req}
	case errors.Is(err, os.ErrPermission):
		return &BrowseNotFoundError{Path: req, Cause: os.ErrPermission}
	}
	return err
}

// IsConnectionError reports whether err from Browse suggests the underlying
// SSH/SFTP connection is dead (as opposed to a well-formed "no such
// directory" or a server-side status), so the caller should discard it.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var nf *BrowseNotFoundError
	var st *sftp.StatusError
	return !errors.As(err, &nf) && !errors.As(err, &st)
}
