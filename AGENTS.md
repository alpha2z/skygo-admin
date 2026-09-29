# Skygo Admin contributor instructions

This is an independent public project. Keep runtime files, deployment credentials,
private application code and application data outside this repository.

- Use the pinned public Skygo module; no adjacent workspace replacements.
- Preserve administrator authorization, CSRF, operation confirmation, independent
  approval, signed commands, expiry and durable receipt semantics.
- Agent operations must remain constrained by local service inventory; do not
  introduce remote shell commands, arbitrary mounts or credential-bearing tasks.
- Keep schema migrations explicit and retain task journals across upgrades.
- Run `make test`, `python3 scripts/integration.py`, `make scan` and `make release`.
- Report unverified integrations and build/environment failures separately from
  passing tests. Never print a secret value in scan output or troubleshooting logs.
