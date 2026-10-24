# Synthetic integration harness

Goal: provide a locally runnable MySQL fixture for M1 read-only inventory.

Load-bearing: only loopback database port; randomly generated local credentials;
SELECT-only inventory account; read-only inventory must preserve all fixture
rows; no business API startup or automatic application migrations.

Out of scope: real tenant data, production connectivity, feature switches,
Fleet runtime integration, LLM services, complete production schema validation.

Acceptance: isolated MySQL starts and becomes healthy; fixture contains eight
resources; attempted write is denied; M1 output has expected findings and
unchanged database dump; visibility distribution totals and known negative
cases match; inventory/CLI unit checks and final build/full test succeed.
The dedicated project/port must not alter the earlier fixture environment.
