# Terminal receipt migration without multiStatements

## Goal
Allow startup migrations with the default MySQL multiStatements=false configuration.

## Background
20261027-01-card-terminal-receipts.sql grouped SET, PREPARE, EXECUTE and DEALLOCATE into one driver execution, causing MySQL 1064 at PREPARE.

## Load-bearing list
- Split independent statements using sql-migrate parsing while retaining its transaction connection for session variables and prepared statements.
- Preserve the migration ID, conditional foreign-key removal, existing receipts and refusal of destructive Down.
- Keep application database settings unchanged.

## Out of scope
Deployment credentials, enabling multiStatements, unrelated migration or API changes.

## Acceptance
- Reproduce the original failure with multiStatements=false on isolated MySQL 8.
- Full migration chain and terminal-receipt replay pass without multiStatements; historical receipts survive and unsafe Down stays rejected.
- Parsing regression protects four execution units and transactional connection ownership.
- Go tests and build pass.
