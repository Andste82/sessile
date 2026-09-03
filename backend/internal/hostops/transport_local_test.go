package hostops

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalExecCapturesStdoutStderrAndExitCode(t *testing.T) {
	tr := NewLocal()

	res, err := tr.Exec(context.Background(), "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "out\n" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "out\n")
	}
	if string(res.Stderr) != "err\n" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "err\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}

	res, err = tr.Exec(context.Background(), "exit 7")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
}

func TestLocalFilesResolveIsANoop(t *testing.T) {
	files := NewLocal().Files()
	got, err := files.Resolve(context.Background(), "/already/absolute")
	if err != nil || got != "/already/absolute" {
		t.Fatalf("Resolve = (%q, %v), want unchanged", got, err)
	}
}

func TestLocalFilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	files := NewLocal().Files()

	if err := files.Write(ctx, filepath.Join(dir, "a.txt"), []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := files.List(ctx, dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" || entries[0].IsDir || entries[0].Size != 5 {
		t.Fatalf("List = %+v, want one file a.txt size 5", entries)
	}

	data, err := files.Read(ctx, filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("Read = %q, want %q", data, "hello")
	}

	stat, err := files.Stat(ctx, filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Name != "a.txt" || stat.IsDir || stat.Size != 5 {
		t.Errorf("Stat = %+v, want name=a.txt isDir=false size=5", stat)
	}

	if err := files.Copy(ctx, filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if data, err := files.Read(ctx, filepath.Join(dir, "b.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("Read copy = %q, %v", data, err)
	}

	if err := files.Rename(ctx, filepath.Join(dir, "b.txt"), filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("b.txt still exists after rename")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Errorf("c.txt missing after rename: %v", err)
	}
}

func TestLocalFilesOpenStreamsTheSameContentAsRead(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	files := NewLocal().Files()

	if err := files.Write(ctx, filepath.Join(dir, "a.txt"), []byte("streamed content")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rc, err := files.Open(ctx, filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(Open result): %v", err)
	}
	if string(data) != "streamed content" {
		t.Errorf("Open content = %q, want %q", data, "streamed content")
	}
}

func TestLocalRemoveDeletesNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	files := NewLocal().Files()

	nested := filepath.Join(dir, "sub", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := files.Write(ctx, filepath.Join(nested, "leaf.txt"), []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := files.Remove(ctx, filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Errorf("sub still exists after Remove")
	}
}
