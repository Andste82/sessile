// Package upgrade keeps a data directory safe across releases (§8, §12e):
// a backup of everything stateful before anything rewrites it, and a small
// state file that records which release and which directory layout last
// touched the directory. It knows file names, not formats — the database
// snapshot is handed in by internal/storage, which owns the connection.
package upgrade

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// KeepBackups is how many backups survive pruning. Every one holds a copy of
// users.yml and hosts.yml credentials, so keeping them all forever would only
// multiply what has to be protected.
const KeepBackups = 3

// backupTimeFormat is ISO 8601 basic format, not RFC 3339: a colon is not a
// legal file-name character on Windows or in an SMB share, and a data
// directory bind-mounted from Docker Desktop on Windows is exactly where
// these files land. It sorts the same way the instants do.
const backupTimeFormat = "20060102T150405Z"

// backupName matches a completed backup directory. A ".partial" one is an
// interrupted backup and never counts as one of the KeepBackups.
var backupName = regexp.MustCompile(`^\d{8}T\d{6}Z-[a-z0-9.-]+$`)

// BackupsDir is where Backup writes, under dataDir.
func BackupsDir(dataDir string) string {
	return filepath.Join(dataDir, "backups")
}

// Backup writes one backup of dataDir's state to
// <dataDir>/backups/<UTC time>-<label>/ and returns that directory:
//
//	sessions.db               via snapshotDB (VACUUM INTO), if non-nil
//	config.yml, users.yml     copied, where they exist
//	users/<id>/hosts.yml      copied, every one that exists
//
// Directories are 0700 and files 0600: users.yml has password hashes and
// hosts.yml has plaintext SSH credentials (§11), and a backup must not be
// easier to read than the original.
//
// The backup is assembled under a ".partial" name and renamed into place
// only once complete, so a crash or a failure never leaves something that
// looks like a usable backup. On success, older backups beyond KeepBackups
// are pruned. Scrollback and history are not included — they are large,
// rewritten constantly, and nothing a migration touches.
func Backup(dataDir, label string, snapshotDB func(dest string) error, now time.Time) (string, error) {
	root := BackupsDir(dataDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", root, err)
	}

	final, err := freeName(root, now.UTC().Format(backupTimeFormat)+"-"+label)
	if err != nil {
		return "", err
	}
	partial := final + ".partial"
	if err := os.Mkdir(partial, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(partial)
		}
	}()

	if snapshotDB != nil {
		dest := filepath.Join(partial, "sessions.db")
		if err := snapshotDB(dest); err != nil {
			return "", fmt.Errorf("back up sessions.db: %w", err)
		}
		if err := os.Chmod(dest, 0o600); err != nil {
			return "", fmt.Errorf("back up sessions.db: %w", err)
		}
	}

	files := []string{"config.yml", "users.yml"}
	hostFiles, err := filepath.Glob(filepath.Join(dataDir, "users", "*", "hosts.yml"))
	if err != nil {
		return "", fmt.Errorf("list hosts.yml files: %w", err)
	}
	for _, f := range hostFiles {
		rel, err := filepath.Rel(dataDir, f)
		if err != nil {
			return "", fmt.Errorf("list hosts.yml files: %w", err)
		}
		files = append(files, rel)
	}
	for _, rel := range files {
		if err := copyPrivate(filepath.Join(dataDir, rel), filepath.Join(partial, rel)); err != nil {
			return "", fmt.Errorf("back up %s: %w", rel, err)
		}
	}

	if err := os.Rename(partial, final); err != nil {
		return "", fmt.Errorf("finish backup: %w", err)
	}
	ok = true

	if err := prune(root, KeepBackups); err != nil {
		// The new backup is complete; failing to delete an old one is not a
		// reason to refuse the migration it protects.
		return final, fmt.Errorf("%w: %v", ErrPruneFailed, err)
	}
	return final, nil
}

// ErrPruneFailed wraps a failure to remove old backups. Backup still returns
// the new, complete backup alongside it.
var ErrPruneFailed = errors.New("backup written, but pruning old backups failed")

// freeName returns root/name, or root/name-2, -3, … if a backup of that name
// already exists — two backups within the same second (a migration and an
// adopt-sessions run, say) must not collide.
func freeName(root, name string) (string, error) {
	for i := 1; i < 100; i++ {
		candidate := filepath.Join(root, name)
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d", candidate, i)
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			if _, err := os.Lstat(candidate + ".partial"); os.IsNotExist(err) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("no free backup name for %s in %s", name, root)
}

// copyPrivate copies src to dst with mode 0600, creating dst's parents with
// 0700. A missing src is not an error: a data directory from v0.3 has no
// users.yml, and that is a complete answer.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prune removes the oldest completed backups in root until keep remain.
// Anything not named like a backup is left alone.
func prune(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	return nil
}
