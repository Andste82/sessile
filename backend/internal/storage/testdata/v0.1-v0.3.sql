-- sessions.db as written by sessile 0.3.2, dumped with Python's
-- sqlite3.Connection.iterdump() (§12e M31). Not hand-written: the release
-- image created the sessions over its own API and was stopped normally, so
-- the CREATE TABLE text is what SQLite really stores for that release —
-- including, from v0.5 on, the ", user_id ..." tail that ALTER TABLE leaves.
-- user_version is 0, as in every database from before versioning.
-- Also stands for v0.1.x–v0.3.1: the table did not change before v0.5.0.
-- One row is added by hand with status 'running', the state a crashed
-- server leaves behind, so the reconcile on Open is covered too.
BEGIN TRANSACTION;
CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  directory     TEXT NOT NULL,
  shell         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'running',
  created       TEXT NOT NULL,
  last_activity TEXT NOT NULL
);
INSERT INTO "sessions" VALUES('e7947b55-cd96-486a-bdec-f17e12331b15','build','project-a','bash','stopped','2026-10-18T19:00:02Z','2026-10-18T19:00:02Z');
INSERT INTO "sessions" VALUES('431c2938-4113-4d5a-a275-2a90e670d35a','logs','.','bash','stopped','2026-10-18T19:00:02Z','2026-10-18T19:00:02Z');
INSERT INTO "sessions" VALUES('crashed-0000-0000-0000-000000000000','crashed','project-a','bash','running','2026-10-18T19:00:02Z','2026-10-18T19:00:02Z');
COMMIT;
