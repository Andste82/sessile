-- sessions.db as written by sessile 0.6.0, dumped with Python's
-- sqlite3.Connection.iterdump() (§12e M31). Not hand-written: the release
-- image created the sessions over its own API and was stopped normally, so
-- the CREATE TABLE text is what SQLite really stores for that release —
-- including, from v0.5 on, the ", user_id ..." tail that ALTER TABLE leaves.
-- user_version is 0, as in every database from before versioning.
-- Also stands for v0.6.1 and v0.7.0, which did not change the schema.
BEGIN TRANSACTION;
CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  directory     TEXT NOT NULL,
  shell         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'running',
  created       TEXT NOT NULL,
  last_activity TEXT NOT NULL
, user_id TEXT NOT NULL DEFAULT '', target_type TEXT NOT NULL DEFAULT 'local', host_id TEXT NOT NULL DEFAULT '', host_display_name TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '');
INSERT INTO "sessions" VALUES('c4b82559-943b-408d-87f6-d6d4cdebb6b1','build','project-a','bash','stopped','2026-10-18T19:00:09Z','2026-10-18T19:00:09Z','9eec50f7-0deb-4d9f-b223-9d3bdd43bb58','local','','','infra');
INSERT INTO "sessions" VALUES('2917abae-81c0-4d14-bcd8-771af6b8f40f','logs','.','bash','stopped','2026-10-18T19:00:09Z','2026-10-18T19:00:09Z','9eec50f7-0deb-4d9f-b223-9d3bdd43bb58','local','','','');
COMMIT;
