# Skill Soft Delete

## Goal

Change Skill deletion from physical row removal to logical deletion while
preserving version history and stored artifacts.

## Load-Bearing Behavior

- Live Skill rows have `is_deleted = 0`.
- Deleted Skill rows have `is_deleted = 1` and are excluded from public, owner,
  admin, download, SKILL.md, and version-history reads.
- Delete updates `is_deleted` and `updated_at`; it does not delete
  `skill_versions` or object-storage artifacts.
- Deleted rows do not reserve the live `(owner_id, space_id, name)` uniqueness
  slot, so a user can recreate a Skill with the same name in the same Space.
- Updates and reuploads must not modify deleted Skills.

## Out Of Scope

- Restore/undelete endpoints.
- Artifact garbage collection for deleted Skills.
- Exposing deleted Skills in admin APIs.

## Acceptance Criteria

- Deleting a Skill marks it deleted instead of removing DB rows.
- A second delete of the same Skill is treated as not found.
- Deleted Skills are not returned by normal or admin reads.
- Creating a same-name Skill after deletion does not hit the live-name unique
  constraint.
