package session

import (
	"fmt"
	"os"
	"path/filepath"
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
