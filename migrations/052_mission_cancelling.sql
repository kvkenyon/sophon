-- SQLite cannot add a value to the original mission-state CHECK constraint.
-- Rebuilding the table would retarget all established foreign keys, so amend
-- the constraint definition in place and let a reopened SQLite connection
-- reload it from sqlite_master.
PRAGMA writable_schema = ON;
UPDATE sqlite_master
SET sql = replace(sql,
    "'active', 'completing', 'completed', 'cancelled'",
    "'active', 'completing', 'cancelling', 'completed', 'cancelled'")
WHERE type = 'table' AND name = 'missions';
PRAGMA writable_schema = OFF;
