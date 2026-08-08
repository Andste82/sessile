package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolveDir validates a user-supplied directory against the sandbox root and
// returns the absolute, symlink-resolved path to use as the shell's working
// directory. This is security-critical — every user-supplied path must pass
// through here (PROJECT_PLAN.md §4.5).
//
// Rules:
//  1. Reject empty, absolute, or ".."-containing paths.
//  2. Join under root and clean.
//  3. Resolve symlinks; the target must exist.
//  4. The resolved path must equal the resolved root or live beneath it.
//  5. The target must be a directory.
func resolveDir(root, userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("directory is empty")
	}
	if filepath.IsAbs(userPath) {
		return "", fmt.Errorf("directory must be relative")
	}
	// Reject any ".." segment outright (defense in depth; EvalSymlinks below
	// also guards against escapes via symlinks).
	for _, seg := range strings.Split(filepath.ToSlash(userPath), "/") {
		if seg == ".." {
			return "", fmt.Errorf("directory must not contain '..'")
		}
	}

	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}

	full := filepath.Join(rootResolved, filepath.Clean(userPath))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}

	if resolved != rootResolved &&
		!strings.HasPrefix(resolved, rootResolved+string(os.PathSeparator)) {
		return "", fmt.Errorf("directory escapes sandbox root")
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat directory: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return resolved, nil
}

// relativeToRoot expresses an absolute path as one relative to the sandbox
// root, or returns "" when it lies outside. The root itself is ".", matching
// how the directory listing names it (§6).
//
// This runs the §4.5 containment rule in the other direction. The path here
// does not come from the user but from the kernel (/proc/<pid>/cwd), and it is
// on its way out to a browser rather than into a shell — but a session whose
// shell has cd-ed past the root must not have that path displayed either, and
// "inside the sandbox" deserves one definition rather than two.
//
// No EvalSymlinks on abs: the kernel already hands back a fully resolved path.
// The root still needs resolving, because it is the one the caller configured.
func relativeToRoot(root, abs string) string {
	if abs == "" || !filepath.IsAbs(abs) {
		return ""
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	if abs == rootResolved {
		return "."
	}
	prefix := rootResolved + string(os.PathSeparator)
	if !strings.HasPrefix(abs, prefix) {
		return ""
	}
	return filepath.ToSlash(strings.TrimPrefix(abs, prefix))
}

// ListDirs returns the names of the immediate subdirectories of a sandboxed
// path. userPath is relative to root ("" or "." means the root) and is
// validated through the same sandbox check as session creation (§4.5), so
// callers may pass user input directly. Hidden entries (including the internal
// state dir) are omitted; results are sorted.
func ListDirs(root, userPath string) ([]string, error) {
	if userPath == "" {
		userPath = "."
	}
	resolved, err := resolveDir(root, userPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}
