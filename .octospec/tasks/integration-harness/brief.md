# Synthetic integration harness

Goal: provide a locally runnable MySQL fixture for M1 read-only inventory and
commands for R permission fixtures / I installation primitive checks.

Load-bearing: only loopback database port; randomly generated local credentials;
SELECT-only inventory account; read-only inventory must preserve all fixture
rows; no business API startup or automatic application migrations.

Out of scope: real tenant data, production connectivity, feature switches,
Fleet runtime integration, LLM services, complete production schema validation.

Acceptance: isolated MySQL starts and becomes healthy; fixture contains four
resources; attempted write is denied; M1 output has expected findings and
unchanged database dump; R/I unit checks and final build/full test succeed on
the combined local branch.
