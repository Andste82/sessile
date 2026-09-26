package tasks

import (
	"fmt"
	"os"
	"path/filepath"
)

// localFS writes an agent's folder on the server's own disk. Since v0.9 that
// is the only place sessile renders a task's files: the host gets the repo
// and nothing else sessile has to write (§4.12.2). The path is sessile's own,
// under --data-dir, never one a caller supplied.
type localFS struct{}

func (localFS) Join(elem ...string) string { return filepath.Join(elem...) }
func (localFS) MkdirAll(p string) error    { return os.MkdirAll(p, 0o700) }
func (localFS) Close() error               { return nil }

func (localFS) Chmod(p string, perm os.FileMode) error { return os.Chmod(p, perm) }

func (localFS) WriteFile(p string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", p, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", p, err)
	}
	return f.Close()
}
