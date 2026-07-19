# Global Skill Tags

## Goal

Allow Skill tags created by administrators to be shared across all Spaces while
preserving Space-local tags created by normal Skill create/update flows.

## Load-Bearing Behavior

- `skill_tags.space_id = ''` is the global tag bucket.
- Normal Skill tag writes remain scoped to the caller's Space.
- Listing tags for a Space returns both global tags and that Space's local tags.
- If a global tag and Space-local tag have the same name, the Space-local tag
  wins in the returned metadata.
- Admin create, update, and reupload flows sync their tags into the global tag
  bucket.

## Out Of Scope

- Changing `skills.space_id` or `skill_tags.space_id` to nullable.
- Adding separate tag management endpoints.
- Changing Skill visibility semantics.

## Acceptance Criteria

- `/skills/tags` returns current-Space tags plus global admin tags.
- Admin-created and admin-updated Skill tags become visible to every Space.
- Existing user Skill create/update tag indexing remains Space scoped.
- Repository and service tests cover global tag listing and admin tag sync.
