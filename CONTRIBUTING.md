# Contributing

Keep the three components independent of application code and adjacent checkouts.
Use synthetic fixtures, local example addresses and generated test credentials.
Never include runtime files, logs, customer data or private deployment details.

Run `make test`, `python3 scripts/integration.py`, `make scan` and `make release`.
For task or transport changes, add failure/restart tests and run the race detector.
Use `gofmt`; document protocol changes and explicit schema migrations. Preserve
third-party notices. Security concerns should follow SECURITY.md rather than a
public issue containing credentials or exploit details.
