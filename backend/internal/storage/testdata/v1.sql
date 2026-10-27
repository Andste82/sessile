-- sessions.db at schema version 1: the last shape before per-session
-- environment variables (issue #123). Same CREATE TABLE text as the v0.6/v0.7
-- release (migration step 1 leaves exactly that), stamped user_version 1 as a
-- release with the migration framework writes it.
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
INSERT INTO "sessions" VALUES('11111111-0000-4000-8000-000000000001','build','project-a','bash','stopped','2026-10-18T19:00:09Z','2026-10-18T19:00:09Z','9eec50f7-0deb-4d9f-b223-9d3bdd43bb58','local','','','infra');
INSERT INTO "sessions" VALUES('11111111-0000-4000-8000-000000000002','remote','/srv','bash','running','2026-10-18T19:00:09Z','2026-10-18T19:00:09Z','9eec50f7-0deb-4d9f-b223-9d3bdd43bb58','ssh','h1','prod','');
PRAGMA user_version = 1;
COMMIT;
