# Security

Do not post credentials, deployment configurations or runtime logs in public issues.
Use the repository's private vulnerability reporting feature when enabled. Until
that channel is configured, contact the maintainer privately before sharing details.

The agent is a host-administration component. Protect local inventory, signing
trust, state directories and Docker access. Use HTTPS outside explicitly enabled
loopback development. Revoke a compromised host and replace its local credentials;
re-enrollment should use a new host ID so stale signed commands cannot be replayed.

Automatic source scans complement, but do not replace, manual review of code,
fixtures, documentation, images, archives and Git history before publication.
