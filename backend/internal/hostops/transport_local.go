package hostops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// localTransport runs commands and touches files on this process's own
// machine — the sessile server's own host, which only ever ships for Linux
// (PROJECT_PLAN.md §2), so there is no "local Windows" case to support.
type localTransport struct{}

// NewLocal returns a Transport for the local machine.
func NewLocal() Transport { return localTransport{} }

func (localTransport) Exec(ctx context.Context, line string) (Result, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			err = nil
		}
	}
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}, err
}

func (localTransport) Files() FileTransport { return localFileTransport{} }

// localFileTransport is a thin wrapper over stdlib os.* — local paths are
// OS-native (filepath, not path), since the server this runs on is always
// the machine whose filesystem it's touching.
type localFileTransport struct{}

func (localFileTransport) Stat(_ context.Context, path string) (DirEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return DirEntry{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return DirEntry{Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (localFileTransport) List(_ context.Context, path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", filepath.Join(path, e.Name()), err)
		}
		out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

func (localFileTransport) Read(_ context.Context, path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func (localFileTransport) Write(_ context.Context, path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (localFileTransport) Rename(_ context.Context, oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", oldpath, newpath, err)
	}
	return nil
}

func (localFileTransport) Remove(_ context.Context, path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func (t localFileTransport) Copy(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}
