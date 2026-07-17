-- +migrate Up

-- Skill names are unique per owner and Space. Parse-time checks remain useful
-- for early feedback, but only this constraint closes concurrent create,
-- create-time override, and rename races.
ALTER TABLE skills
  ADD UNIQUE KEY uq_skill_owner_space_name (owner_id, space_id, name);

-- +migrate Down

ALTER TABLE skills
  DROP INDEX uq_skill_owner_space_name;
