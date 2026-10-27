# Changelog

Every release gets a section here. Each one has an **Upgrade / Breaking**
part, and that part is the thing to read before you pull a new image: it
lists every change that needs something from you, such as a moved volume, a
removed flag, or data that will not show up without an extra step. "None"
means the new version starts on the old data with nothing for you to do.

Skipping versions is fine, but read the Upgrade / Breaking part of **every**
release in between, not just the newest one. How to upgrade safely is in the
README under [Upgrading](README.md#upgrading).

## Unreleased

Upgrade / Breaking (environment variables, issue #123): this release adds
schema migration step 2 (new `env_vars`, `env_secrets` and `dotenv` columns
on `sessions`). **Releases older than this one refuse to open the migrated
database**, so going back means restoring the pre-migration backup under
`<data-dir>/backups/*-schema-v1/`. New plaintext secrets: `users/<id>/env.yml`
(default environment variables), the sessions database and `backups/` all
hold environment variable values, including ones marked secret, in plaintext.
Protect the data directory accordingly.

- Sessions can carry environment variables, set per session in the new
  session dialog (editable while the session is stopped) and as per-user
  defaults in Settings. A session can also load a `.env` file from its start
  directory on every start. Secret values are write-only: never returned by
  the API, sent in events or logged.

Downgrades: the guard below only exists from this release on. 0.7.0 and
older do not read the schema version, so nothing stops them from starting
on a database a newer release has migrated. Going back to 0.7.0 from this
release only works if the database is still at schema version 1; with the
environment variable migration (step 2, below) applied, going back past it
means restoring a backup.

- `sessions.db` records its schema version (`PRAGMA user_version`), and
  every schema change is a numbered migration step that is applied
  completely or not at all. A release refuses to start on a database a
  newer release has already migrated, instead of writing into a schema it
  does not know.
- Before every migration, the server backs up `sessions.db`, `config.yml`,
  `users.yml` and every `hosts.yml` to `<data-dir>/backups/<time>-schema-v<n>/`
  and keeps the newest three. If the backup fails, nothing is migrated and
  the server does not start.
- New file `<data-dir>/.sessile-state.yml` records the release that last
  ran on the directory. Do not edit it.
- A start on a data directory with no state at all logs a loud warning with
  the resolved path. After an upgrade, that almost always means a volume
  that is not mounted where it used to be. Sessions without an owner (from
  0.3.x and older) are counted in a warning on every start.
- New command `sessile adopt-sessions --data-dir=<dir> --user=<username>`
  hands every session without an owner to one user. Run it while the server
  is stopped. It takes a backup first, and a second run changes nothing. See
  0.5.0, step 3.

### Upgrade / Breaking

None.

## 0.7.0 — 2026-09-14

- The open terminal tabs are restored after a page reload.
- Mobile: the key bar and bottom nav follow the on-screen keyboard. The
  "Keys" toggle is gone.

### Upgrade / Breaking

None.

## 0.6.1 — 2026-09-12

- Mobile: the key bar scrolls instead of making the terminal wider than the
  screen.

### Upgrade / Breaking

None.

## 0.6.0 — 2026-09-08

- Session groups: give a session a group label, and see groups as
  collapsible sections on the dashboard and in the sidebar.
- The files & processes panel belongs to its session tab and opens in the
  session root.
- Every password field has a show/hide toggle.

### Upgrade / Breaking

None. The sessions table gains a `group_name` column on first start; existing
sessions come back without a group.

## 0.5.0 — 2026-09-07

Multi-user accounts, per-user SSH hosts, and host operations (process tree,
file browser, copy/move/delete, download/upload). There was no 0.4.0 release;
its contents ship here.

### Upgrade / Breaking

This is the release that needs work by hand. Coming from 0.3.x or older, do
all four steps below **before** starting 0.5.0 or later for the first time.

1. **The compose file uses bind mounts now, not a named volume.**
   0.3.x stored `/config` in the named volume `sessile-config` and the
   workspace in `./workspace`. From 0.5.0 on, the compose file mounts
   `./data/config:/config` and `./data/workspace:/workspace`. If you switch to
   the new compose file without moving anything, the container starts on an
   **empty** directory: no sessions, and the server asks for a new admin
   account. Your old data is not deleted. It is still in the volume, which
   is easy to miss.

   Copy it over once, from the directory that holds your compose file:

   ```bash
   docker compose down
   mkdir -p data/config data/workspace
   # Copies /old into /new, but only into an empty /new, and lists the result.
   copy='[ -z "$(ls -A /new)" ] || { echo "/new is not empty, nothing copied"; exit 1; }
         cp -a /old/. /new/ && ls -A /new'
   # Compose prefixes the volume with the project name, which defaults to
   # the directory name. `docker volume ls | grep sessile-config` shows it.
   docker run --rm -v <project>_sessile-config:/old -v "$PWD/data/config:/new" \
     alpine sh -c "$copy"
   # The workspace moved too. Skip this if ./workspace does not exist.
   docker run --rm -v "$PWD/workspace:/old" -v "$PWD/data/workspace:/new" \
     alpine sh -c "$copy"
   ```

   The first listing must show `sessions.db`. If it is empty, stop: the
   volume name is wrong. The copy refuses a non-empty target instead of
   merging into it, so running it twice is harmless. Do not reach for
   `cp -an` here: with `-n`, BusyBox `cp` (which the alpine image has)
   silently copies nothing into an existing directory and still exits 0.
   Remove the old volume only after the new version has started and your
   data is there (`docker volume rm <project>_sessile-config`).

2. **`--root` and `--db` are gone.** If you wrote your own `command:` or
   start the binary by hand, change these flags or the server will not start
   ("flag provided but not defined"):

   | Old | New |
   |---|---|
   | `--root=<dir>` | `--workspace-dir=<dir>` |
   | `--db=<dir>/sessions.db` | `--data-dir=<dir>` |
   | `--dev` | `--insecure-cookies --allow-origin=http://localhost:5173` |

   The image's default command is already up to date. If you never
   overrode it, there is nothing to change here.

3. **Old sessions have no owner.** Accounts are new in this release, and
   sessions created before it have no user. Every session belongs to exactly
   one user, so these show up for nobody, not even the admin. They are still
   in `sessions.db`, together with their scrollback and history. From the
   release after 0.7.0 on, the `adopt-sessions` command hands them to a
   user: create the admin account, stop the server, then run

   ```bash
   docker compose run --rm sessile adopt-sessions --data-dir=/config --user=<username>
   ```

   Releases 0.5.0 to 0.7.0 have no way to show them. Keep them in
   `sessions.db` (and a backup of `/config`) until you are on a release
   that has the command.

4. **Local shells are off by default.** Every session from 0.3.x is a local
   shell on the server itself, and the new `allowLocalHost` setting starts
   out off. Turn it on under Settings (as admin) or in `config.yml` before
   you restart one of the old sessions.

## 0.3.2 — 2026-08-23

- The dashboard shows what each session is doing (foreground program,
  activity state) and stays live over the new `/ws/events` channel.
- Restored scrollback no longer replays terminal queries into the new shell,
  and the restore separator turns leftover input modes off.

### Upgrade / Breaking

None.

## 0.3.1 — 2026-08-09

- The selection is copied as it is made.
- Mobile: glide typing keeps the spaces between words.

### Upgrade / Breaking

None.

## 0.3.0 — 2026-08-05

- The terminal renders on the GPU and falls back to the DOM renderer.
- Mobile: scrolling also reaches full-screen programs, not only the backlog.
- A session is sized to its smallest client.

### Upgrade / Breaking

None.

## 0.2.1 — 2026-08-05

- Mobile scrolling fixes.

### Upgrade / Breaking

None.

## 0.2.0 — 2026-08-04

- Stopped sessions can be restarted with their scrollback and command
  history.
- The window title follows the active session. The terminal font size can
  be set in Settings.

### Upgrade / Breaking

- **`--db` was replaced by `--data-dir`.** The flag now names the state
  directory instead of a database file inside it. Change
  `--db=/config/sessions.db` to `--data-dir=/config`.

## 0.1.x — 2026-07

First releases: persistent PTY sessions in the browser, reconnect with
replay, tabs, mobile layout, Docker image (alpine and ubuntu variants).
0.1.0 and 0.1.4 were tagged but never published.
