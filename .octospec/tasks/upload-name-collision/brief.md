# Upload creation name collisions
Goal: Skill, Expert and Expert Team creation rejects normalized display names already reserved in the same Space or present in the visible global market, with a rename instruction.
Load-bearing: transactional create/import/container paths, name reservation uniqueness, legacy rows, published-vs-draft names, Space isolation.
Out of scope: connector naming, same-version visibility workflow, remote pushes/PRs and local image deployment.
Acceptance: all three types reject duplicate/case/full-width names, including published names after draft rename and legacy NULL-key rows; other Spaces and different types remain independent; embedded children remain exempt; concurrency cannot create same-Space duplicates; imported-task rollback remains retryable; 409 DUPLICATE names plugin_name and instructs rename.
