-- sessions.db as written by sessile 0.5.0, dumped with Python's
-- sqlite3.Connection.iterdump() (§12e M31). Not hand-written: the release
-- image created the sessions over its own API and was stopped normally, so
-- the CREATE TABLE text is what SQLite really stores for that release —
-- including, from v0.5 on, the ", user_id ..." tail that ALTER TABLE leaves.
-- user_version is 0, as in every database from before versioning.
BEGIN TRANSACTION;
CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  directory     TEXT NOT NULL,
  shell         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'running',
  created       TEXT NOT NULL,
  last_activity TEXT NOT NULL
, user_id TEXT NOT NULL DEFAULT '', target_type TEXT NOT NULL DEFAULT 'local', host_id TEXT NOT NULL DEFAULT '', host_display_name TEXT NOT NULL DEFAULT '');
INSERT INTO "sessions" VALUES('49785ada-aa2a-4736-94b2-03a860f056fa','build','project-a','bash','stopped','2026-10-18T19:00:06Z','2026-10-18T19:00:06Z','d02a1eb2-1b44-4200-b40c-2451058e9f62','local','','');
INSERT INTO "sessions" VALUES('de2b5bc8-dab9-4f62-b9fd-8758466bb6a9','logs','.','bash','stopped','2026-10-18T19:00:06Z','2026-10-18T19:00:06Z','d02a1eb2-1b44-4200-b40c-2451058e9f62','local','','');
COMMIT;
