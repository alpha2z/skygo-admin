# Management UI compatibility during rollback

A live browser test upgraded actual API/Web containers from v0.1.0 to v0.2.0,
then rolled the pair back. The signed execution and container restoration
succeeded, but a refreshed browser could still use the newer frontend, whose
extension catalog request received 404 from the older API. Initialization then
stopped at the login screen despite a valid session.

The frontend now treats only an explicit catalog 404 as a legacy API with no
extensions. Authentication, permission and availability failures still stop
initialization. UI resources served by the API, authenticated extension assets
and the packaged Web proxy send `Cache-Control: no-store`. Public artifact
locations retain their existing cache contract, and proxy security headers are
preserved.

Local validation covered the real browser submission and independent approval
flow, API-only and Web-only updates, paired update and rollback, a deliberately
unhealthy API image, recovery while the API was offline, browser reload of the
original publication, Agent restart without duplicate execution, and container
recreation using persisted image variables. The catalog compatibility and
cache-header regressions also run in the existing frontend/MySQL test suites.

The fixture used a disposable database, a loopback registry and synthetic signed
build records. It did not validate external build-provider access or SMTP delivery;
email confirmation was explicitly disabled for the fixture, while separate
administrator approval and signed Agent commands remained enabled.
