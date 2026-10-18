package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/upgrade"
)

// openStore is the one way into a data directory's database, shared by the
// server and adopt-sessions (§8, §12e): refuse a directory or schema a newer
// release has already upgraded, and back the directory up before any
// migration step runs. It returns the state file as it was before this
// start, for the caller to log and rewrite.
func openStore(dataDir, dbPath string, log *slog.Logger) (*storage.Store, upgrade.State, error) {
	state, _, err := upgrade.ReadState(dataDir)
	if err != nil {
		return nil, upgrade.State{}, err
	}
	if err := upgrade.CheckLayout(dataDir, state); err != nil {
		return nil, upgrade.State{}, fmt.Errorf("%w — run that release again, or restore a backup from %s",
			err, upgrade.BackupsDir(dataDir))
	}

	store, err := storage.OpenWith(dbPath, storage.Options{
		BeforeMigrate: func(p storage.Pending) error {
			dir, err := upgrade.Backup(dataDir, fmt.Sprintf("schema-v%d", p.From), p.Snapshot, time.Now())
			if errors.Is(err, upgrade.ErrPruneFailed) {
				log.Warn("could not remove old backups", "err", err)
			} else if err != nil {
				return fmt.Errorf("back up before migrating sessions.db from schema %d to %d "+
					"(nothing was migrated): %w", p.From, p.To, err)
			}
			log.Info("backup written before migration", "dir", dir, "from-schema", p.From, "to-schema", p.To)
			return nil
		},
	})
	var tooNew *storage.SchemaTooNewError
	if errors.As(err, &tooNew) {
		return nil, upgrade.State{}, fmt.Errorf("%w — run that release again, or restore a backup from %s",
			err, upgrade.BackupsDir(dataDir))
	}
	if err != nil {
		return nil, upgrade.State{}, fmt.Errorf("open store: %w", err)
	}
	return store, state, nil
}
