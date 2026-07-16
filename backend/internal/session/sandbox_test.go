package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDir(t *testing.T) {
	root := t.TempDir()
	// Build a small tree:
	//   root/project-a
	//   root/project-a/nested
	//   outside/           (sibling of root, target of an escaping symlink)
	//   root/evil -> outside
	mustMkdir(t, filepath.Join(root, "project-a"))
	mustMkdir(t, filepath.Join(root, "project-a", "nested"))

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	rootResolved, _ := filepath.EvalSymlinks(root)

	tests := []struct {
		name    string
		input   string
		wantErr bool
		want    string
	}{
		{name: "valid top-level", input: "project-a", want: filepath.Join(rootResolved, "project-a")},
		{name: "valid nested", input: "project-a/nested", want: filepath.Join(rootResolved, "project-a", "nested")},
		{name: "dot resolves to root", input: ".", want: rootResolved},
		{name: "empty rejected", input: "", wantErr: true},
		{name: "absolute rejected", input: "/etc", wantErr: true},
		{name: "parent traversal rejected", input: "..", wantErr: true},
		{name: "deep traversal rejected", input: "../..", wantErr: true},
		{name: "embedded traversal rejected", input: "project-a/../../etc", wantErr: true},
		{name: "symlink escape rejected", input: "evil", wantErr: true},
		{name: "nonexistent rejected", input: "does-not-exist", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDir(root, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveDir(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDir(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("resolveDir(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
