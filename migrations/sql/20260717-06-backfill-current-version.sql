-- +migrate Up

-- Backfill current_version_id for skills that already have version records.
-- For each skill, pick the most recently created skill_version row.
UPDATE skills s
  JOIN (
    SELECT skill_id, id AS version_id
    FROM skill_versions sv1
    WHERE created_at = (
      SELECT MAX(created_at) FROM skill_versions sv2 WHERE sv2.skill_id = sv1.skill_id
    )
  ) latest ON latest.skill_id = s.id
SET s.current_version_id = latest.version_id
WHERE s.current_version_id = '';

-- For skills that have NO skill_versions record yet, create one from existing
-- legacy columns and backfill the pointer.
-- Uses a stored procedure with cursor iteration; wrapped in StatementBegin/End
-- so sql-migrate sends the entire block as one statement (semicolons inside
-- the procedure body won't cause premature splitting).

-- +migrate StatementBegin
DROP PROCEDURE IF EXISTS backfill_missing_versions;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE PROCEDURE backfill_missing_versions()
BEGIN
  DECLARE done INT DEFAULT FALSE;
  DECLARE v_skill_id VARCHAR(36);
  DECLARE v_version VARCHAR(32);
  DECLARE v_version_id VARCHAR(36);
  DECLARE cur CURSOR FOR
    SELECT s.id, s.version
    FROM skills s
    WHERE s.current_version_id = ''
      AND NOT EXISTS (SELECT 1 FROM skill_versions sv WHERE sv.skill_id = s.id);
  DECLARE CONTINUE HANDLER FOR NOT FOUND SET done = TRUE;

  OPEN cur;
  read_loop: LOOP
    FETCH cur INTO v_skill_id, v_version;
    IF done THEN
      LEAVE read_loop;
    END IF;

    SET v_version_id = UUID();

    INSERT INTO skill_versions (id, skill_id, version, changelog, storage, changed_by)
    VALUES (v_version_id, v_skill_id, v_version, '', NULL, 'system-backfill');

    UPDATE skills SET current_version_id = v_version_id WHERE id = v_skill_id;
  END LOOP;
  CLOSE cur;
END;
-- +migrate StatementEnd

CALL backfill_missing_versions();
DROP PROCEDURE IF EXISTS backfill_missing_versions;

-- +migrate Down

-- Backfill is data-only; we don't remove version rows on rollback,
-- just clear the pointer that was set.
UPDATE skills SET current_version_id = '' WHERE current_version_id != '';
