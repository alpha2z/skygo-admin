# Extension SDK release verification

This release adds public Go composition packages, protected extension routes and
pages, and explicitly authorized Agent actions. Existing standalone APIs and
signed task journals remain the source of authentication, approval and execution.
No schema change is required. See [extension contracts](extensions.md).

Executed locally:

- `make test`: Go packages, race checks, 17 frontend tests and build provenance tests.
- `python3 scripts/integration.py`: disposable MySQL migration/security/task tests,
  including extension capability checks, independent approval, permission revocation,
  pre-dispatch policy changes and duplicate receipts.
- `python3 scripts/docker-smoke.py`: independent startup, packaged resources/404,
  two synthetic framework nodes, cold-cache preparation, single/pair publication,
  health failure rollback and offline/restart recovery without duplicate execution.
- `make scan` and `make release`: source/history/archive policy checks, four Linux
  amd64/arm64 binaries, source manifest and checksums. No matching values are logged.
- The source archive was extracted outside the checkout and tested with `GOWORK=off`
  and `-mod=readonly`, without neighboring application modules.

Deployment inventories and compiled extension implementations are trusted local
configuration, not runtime-uploaded plugins. Extension developers must validate
business payloads, implement conservative reconciliation and keep credentials out
of observation/result data. A composition must pin its own trusted release source.

No production deployment or external provider integration was performed by these
local checks. SMTP, repository access and registry credentials need separate
installation-specific verification.
