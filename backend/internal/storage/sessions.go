package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Andste82/sessile/backend/internal/session"
)

// envRow is one stored environment entry. Short keys keep the column small;
// the secret flag is not stored because it is implied by which column holds
// the row.
type envRow struct {
	N string `json:"n"`
	V string `json:"v"`
	D bool   `json:"d,omitempty"`
}

// marshalEnv splits entries into the env_vars (non-secret) and env_secrets
// columns. Both are always a JSON array, never null.
func marshalEnv(env []session.EnvVar) (vars, secrets string, err error) {
	plain, secret := []envRow{}, []envRow{}
	for _, e := range env {
		r := envRow{N: e.Name, V: e.Value, D: e.Default}
		if e.Secret {
			secret = append(secret, r)
		} else {
			plain = append(plain, r)
		}
	}
	vb, err := json.Marshal(plain)
	if err != nil {
		return "", "", fmt.Errorf("marshal env: %w", err)
	}
	// TODO(security): plaintext at rest, optional encryption later
	sb, err := json.Marshal(secret)
	if err != nil {
		return "", "", fmt.Errorf("marshal env secrets: %w", err)
	}
	return string(vb), string(sb), nil
}

// unmarshalEnv rebuilds Env from the two columns, sorted by name. The error
// never includes the column text: it may hold secrets.
func unmarshalEnv(vars, secrets string) ([]session.EnvVar, error) {
	out := []session.EnvVar{}
	for _, col := range []struct {
		text   string
		secret bool
	}{{vars, false}, {secrets, true}} {
		var rows []envRow
		if err := json.Unmarshal([]byte(col.text), &rows); err != nil {
			return nil, errors.New("decode stored environment")
		}
		for _, r := range rows {
			out = append(out, session.EnvVar{Name: r.N, Value: r.V, Secret: col.secret, Default: r.D})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Insert upserts a session's metadata (used on create and update).
func (s *Store) Insert(i session.Info) error {
	envVars, envSecrets, err := marshalEnv(i.Env)
	if err != nil {
		return err
	}
	dotenv := 0
	if i.DotEnv {
		dotenv = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions (id, name, directory, shell, status, created, last_activity,
		                       user_id, target_type, host_id, host_display_name, group_name,
		                       env_vars, env_secrets, dotenv)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name,
		   directory=excluded.directory,
		   shell=excluded.shell,
		   status=excluded.status,
		   last_activity=excluded.last_activity,
		   user_id=excluded.user_id,
		   target_type=excluded.target_type,
		   host_id=excluded.host_id,
		   host_display_name=excluded.host_display_name,
		   group_name=excluded.group_name,
		   env_vars=excluded.env_vars,
		   env_secrets=excluded.env_secrets,
		   dotenv=excluded.dotenv`,
		i.ID, i.Name, i.Directory, i.Shell, string(i.Status),
		i.Created.UTC().Format(time.RFC3339), i.LastActivity.UTC().Format(time.RFC3339),
		i.UserID, string(i.TargetType), i.HostID, i.HostDisplayName, i.Group,
		envVars, envSecrets, dotenv,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// SetStatus updates a session's lifecycle status.
func (s *Store) SetStatus(id string, status session.Status) error {
	_, err := s.db.Exec(`UPDATE sessions SET status=? WHERE id=?`, string(status), id)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	return nil
}

// Touch updates a session's last-activity timestamp.
func (s *Store) Touch(id string, lastActivity time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_activity=? WHERE id=?`,
		lastActivity.UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes a session row.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// selectColumns lists every column scan expects, in order — shared by Get
// and LoadStopped so the two queries can never drift apart.
const selectColumns = `id, name, directory, shell, status, created, last_activity,
	                       user_id, target_type, host_id, host_display_name, group_name,
	                       env_vars, env_secrets, dotenv`

// Get returns a single session's persisted metadata.
func (s *Store) Get(id string) (session.Info, bool, error) {
	row := s.db.QueryRow(`SELECT `+selectColumns+` FROM sessions WHERE id=?`, id)
	info, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Info{}, false, nil
	}
	if err != nil {
		return session.Info{}, false, err
	}
	return info, true, nil
}

// LoadStopped returns all sessions persisted with stopped status, across
// every user — Manager.List filters to the caller's own in Go, alongside
// the in-memory sessions it merges these with (§4.3).
func (s *Store) LoadStopped() ([]session.Info, error) {
	rows, err := s.db.Query(`SELECT ` + selectColumns + ` FROM sessions WHERE status='stopped'`)
	if err != nil {
		return nil, fmt.Errorf("query stopped: %w", err)
	}
	defer rows.Close()

	var out []session.Info
	for rows.Next() {
		info, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// CountOwnerless returns how many sessions have no owner: rows written before
// accounts existed (§8), invisible to every user until adopted (§12e M34).
func (s *Store) CountOwnerless() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id=''`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count ownerless sessions: %w", err)
	}
	return n, nil
}

// AdoptOwnerless assigns every ownerless session to userID and returns how
// many it changed. It is only ever called by the offline adopt-sessions
// command (§12e M34, §11), with an id resolved from users.yml — never from a
// request, which is why no API route reaches it.
func (s *Store) AdoptOwnerless(userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("adopt ownerless sessions: empty user id")
	}
	res, err := s.db.Exec(`UPDATE sessions SET user_id=? WHERE user_id=''`, userID)
	if err != nil {
		return 0, fmt.Errorf("adopt ownerless sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("adopt ownerless sessions: %w", err)
	}
	return int(n), nil
}

// DeleteStoppedBefore removes stopped sessions whose last activity predates
// cutoff and returns the ids it removed, so the caller can discard their
// scrollback and history too.
//
// The comparison is lexicographic on the stored text. That is exact here and
// not a shortcut: every write formats through time.RFC3339 in UTC, which is
// fixed-width and orders the same way as the instants it encodes.
func (s *Store) DeleteStoppedBefore(cutoff time.Time) ([]string, error) {
	before := cutoff.UTC().Format(time.RFC3339)

	rows, err := s.db.Query(
		`SELECT id FROM sessions WHERE status='stopped' AND last_activity < ?`, before)
	if err != nil {
		return nil, fmt.Errorf("query expired: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan expired: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query expired: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := s.db.Exec(
		`DELETE FROM sessions WHERE status='stopped' AND last_activity < ?`, before); err != nil {
		return nil, fmt.Errorf("delete expired: %w", err)
	}
	return ids, nil
}

// scanner abstracts *sql.Row and *sql.Rows for scan.
type scanner interface {
	Scan(dest ...any) error
}

func scan(sc scanner) (session.Info, error) {
	var (
		info                     session.Info
		status, created, lastAct string
		targetType               string
		envVars, envSecrets      string
		dotenv                   int
	)
	if err := sc.Scan(&info.ID, &info.Name, &info.Directory, &info.Shell,
		&status, &created, &lastAct,
		&info.UserID, &targetType, &info.HostID, &info.HostDisplayName,
		&info.Group, &envVars, &envSecrets, &dotenv); err != nil {
		return session.Info{}, err
	}
	env, err := unmarshalEnv(envVars, envSecrets)
	if err != nil {
		return session.Info{}, err
	}
	info.Env = env
	info.DotEnv = dotenv != 0
	info.Status = session.Status(status)
	info.TargetType = session.TargetType(targetType)
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		info.Created = t
	}
	if t, err := time.Parse(time.RFC3339, lastAct); err == nil {
		info.LastActivity = t
	}
	// PID/Rows/Cols are runtime-only; a persisted (stopped) session has none.
	return info, nil
}
