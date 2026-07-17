-- +migrate Up

-- Add attempts column for stale-task recovery (Poll Lazy Recovery).
-- Tracks how many times a stuck-in-parsing task has been re-submitted.
ALTER TABLE parse_tasks ADD COLUMN attempts INT NOT NULL DEFAULT 0 AFTER status;

-- +migrate Down

ALTER TABLE parse_tasks DROP COLUMN attempts;
