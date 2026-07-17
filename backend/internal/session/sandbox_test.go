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

func TestListDirs(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "project-a"))
	mustMkdir(t, filepath.Join(root, "project-b"))
	mustMkdir(t, filepath.Join(root, "project-a", "nested"))
	mustMkdir(t, filepath.Join(root, ".hidden"))
	mustMkdir(t, filepath.Join(root, ".tsm"))
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Root lists non-hidden directories only, sorted; files excluded.
	got, err := ListDirs(root, "")
	if err != nil {
		t.Fatalf("ListDirs(root) error: %v", err)
	}
	if want := []string{"project-a", "project-b"}; !equalStrings(got, want) {
		t.Fatalf("ListDirs(root) = %v, want %v", got, want)
	}

	// "." is equivalent to the root.
	if dot, _ := ListDirs(root, "."); !equalStrings(dot, got) {
		t.Fatalf(`ListDirs(".") = %v, want %v`, dot, got)
	}

	// Nested navigation.
	nested, err := ListDirs(root, "project-a")
	if err != nil {
		t.Fatalf("ListDirs(project-a) error: %v", err)
	}
	if want := []string{"nested"}; !equalStrings(nested, want) {
		t.Fatalf("ListDirs(project-a) = %v, want %v", nested, want)
	}

	// Traversal and missing paths are rejected by the sandbox check.
	if _, err := ListDirs(root, "../.."); err == nil {
		t.Fatal("ListDirs(../..) expected error")
	}
	if _, err := ListDirs(root, "does-not-exist"); err == nil {
		t.Fatal("ListDirs(does-not-exist) expected error")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
