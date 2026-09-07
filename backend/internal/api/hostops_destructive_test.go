package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/session"
)

// TestResolveDestructiveHostopsPathRejectsTheWorkspaceRoot reproduces the
// review's exact finding: DELETE .../hostops/files?path=. resolves to the
// workspace root itself (session.ResolvePath correctly returns it, since
// listHostFiles needs that), and resolveHostopsPath alone had no guard
// against a destructive caller accepting that same resolution.
func TestResolveDestructiveHostopsPathRejectsTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := &Server{workspaceRoot: root}
	info := session.Info{TargetType: session.TargetLocal}

	for _, userPath := range []string{".", ""} {
		if _, _, err := s.resolveDestructiveHostopsPath(info, userPath); err == nil {
			t.Errorf("resolveDestructiveHostopsPath(%q) = nil error, want rejection of the workspace root", userPath)
		}
	}

	// A real subdirectory must still resolve normally — this isn't a
	// blanket rejection of the whole workspace, only its root.
	resolved, _, err := s.resolveDestructiveHostopsPath(info, "proj")
	if err != nil {
		t.Fatalf("resolveDestructiveHostopsPath(proj): unexpected error: %v", err)
	}
	wantRoot, _ := session.ResolvePath(root, ".")
	if resolved != filepath.Join(wantRoot, "proj") {
		t.Errorf("resolveDestructiveHostopsPath(proj) = %q, want %s/proj", resolved, wantRoot)
	}
}

// TestResolveDestructiveHostopsPathRejectsSSHRootPaths covers the
// unvalidated SSH case: "." and "/" are the two paths that unambiguously
// mean "here" or "everything" if left unresolved.
func TestResolveDestructiveHostopsPathRejectsSSHRootPaths(t *testing.T) {
	s := &Server{}
	info := session.Info{TargetType: session.TargetSSH}

	// Every spelling of "here" and "everything", not just the two canonical
	// ones: resolveHostopsPath hands an SSH path back unnormalized, so a
	// literal comparison let "./" and friends through — and they are not
	// near misses. runDelete builds each victim with path.Join(target, name),
	// and path.Join("./", name) is byte-identical to path.Join(".", name),
	// so "./" deleted precisely what "." was blocked from deleting.
	for _, userPath := range []string{".", "/", "", "./", ".//", "/.", "//", "/./", "./."} {
		if _, _, err := s.resolveDestructiveHostopsPath(info, userPath); err == nil {
			t.Errorf("resolveDestructiveHostopsPath(%q) = nil error, want rejection", userPath)
		}
	}

	// A relative path that merely starts with "./" is a normal target and
	// must still be allowed — the guard rejects the root, not the notation.
	for _, userPath := range []string{"./file.txt", "./dir/sub", "sub/./file"} {
		if _, _, err := s.resolveDestructiveHostopsPath(info, userPath); err != nil {
			t.Errorf("resolveDestructiveHostopsPath(%q): unexpected error: %v", userPath, err)
		}
	}

	if _, _, err := s.resolveDestructiveHostopsPath(info, "/home/user/file.txt"); err != nil {
		t.Errorf("resolveDestructiveHostopsPath(/home/user/file.txt): unexpected error: %v", err)
	}
}

// TestUploadStubPathIsUniquePerUpload pins the other half of the same
// data-loss story: with a fixed ".part" suffix, two uploads to one
// destination stream into the same staging file and the second commit runs
// against a stub the first already renamed away.
func TestUploadStubPathIsUniquePerUpload(t *testing.T) {
	const dest = "/home/user/report.pdf"

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		got := uploadStubPath(dest)
		if !strings.HasPrefix(got, dest+".part") {
			t.Fatalf("stub %q does not sit next to its destination", got)
		}
		if got == dest {
			t.Fatal("stub path equals the destination")
		}
		seen[got] = true
	}
	if len(seen) != 50 {
		t.Errorf("%d distinct stub names out of 50 — concurrent uploads would collide", len(seen))
	}
}
