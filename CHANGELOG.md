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
   # Compose prefixes the volume with the project name, which defaults to
   # the directory name. `docker volume ls | grep sessile-config` shows it.
   docker run --rm -v <project>_sessile-config:/old -v "$PWD/data/config:/new" \
     alpine sh -c 'cp -an /old/. /new/'
   # The workspace moved too. Skip this if ./workspace does not exist.
   cp -an workspace/. data/workspace/
   ```

   `cp -n` never overwrites, so running it twice is harmless. Remove the old
   volume only after the new version has started and your data is there
   (`docker volume rm <project>_sessile-config`).

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
   in `sessions.db`, together with their scrollback and history, but no
   release so far has a way to show them in the UI. Keep a backup of
   `/config` from before the upgrade so they are not lost.

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
