-- +migrate Up

-- Administrator-created Skills use system visibility.
ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private','system') NOT NULL DEFAULT 'space';

-- Normalize the previous representation. Administrator-published Skills carry
-- the empty global Space id; user-published public Skills belong to a real Space.
-- These statements are also the inverse of Down when a deployment is rolled
-- back and later rolled forward.
UPDATE skills SET visibility = 'system' WHERE visibility = 'public' AND space_id = '';
UPDATE skills SET visibility = 'space' WHERE visibility = 'public' AND space_id <> '';

-- +migrate Down

-- Restore the previous representation before removing the ENUM value so the
-- rollback remains valid after administrator-published Skills have been created.
UPDATE skills SET visibility = 'public' WHERE visibility = 'system';

ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private') NOT NULL DEFAULT 'space';
