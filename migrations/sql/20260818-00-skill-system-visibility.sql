-- +migrate Up

-- Administrator-created Skills use system visibility.
ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private','system') NOT NULL DEFAULT 'space';

-- +migrate Down

-- Restore the previous representation before removing the ENUM value so the
-- rollback remains valid after administrator-published Skills have been created.
UPDATE skills SET visibility = 'public' WHERE visibility = 'system';

ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private') NOT NULL DEFAULT 'space';
