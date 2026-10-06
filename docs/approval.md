# Approval policy and schema v6

`ADMIN_INDEPENDENT_APPROVAL_ENABLED` accepts `true` or `false`; absent or empty
means `false`. Invalid values prevent startup. Restart the API after changing it.
This deployment setting is independent of `ADMIN_EMAIL_CONFIRMATION_ENABLED`.

By default, initialize one administrator. Operations require their existing write
permissions and operation confirmation; submission atomically validates the plan,
locks resources and queues execution. An additional approval permission or account
is not required. Previewing a workflow or preparing images remains a separate step.
The image-cleanup submission is an execution confirmation in this mode.

Set the switch to `true` to require independent approval for new tasks,
publications, workflows and cleanups. The requester cannot approve their own
request. Applications should use the SDK policy for their own operations too.

## Explicit migration

Stop the API, back up the management database, and run the matching
`admin-api -migrate` before starting the new API and Web. Ordinary startup and
image upgrades do not migrate. Schema v6 adds `single_confirmation` to tasks,
publications, workflows and image cleanups, with a database default of `false`.
Existing rows retain their historical independent-approval policy. Migration is
idempotent and does not remove accounts, receipts, journals or pending requests.
A v5 API cannot run against v6; a schema rollback requires the verified backup.
Never replay already dispatched commands from a restored database.

## SDK and HTTP behavior

- `Config.IndependentApprovalEnabled` and
  `Server.IndependentApprovalEnabled()` expose the policy for new operations.
- `GET /api/v1/session` includes `independent_approval_enabled`.
- Existing create endpoints and request IDs are retained. Simple-mode submission
  returns the executing record (`queued` or `running`) rather than `pending`.
- Records expose `single_confirmation`. `requested_by` and `approved_by` identify
  the same confirming actor in simple mode; the flag and audit distinguish this
  from independent approval. It never represents a second person.
- Approve/reject endpoints remain for historical and new independent requests.
- `POST /api/v1/{tasks,publications,workflows,image-cleanup}/:id/cancel` cancels
  only an owned pending request with `ops.write`. Queued, dispatched and uncertain
  operations cannot be cancelled this way. Submit a fresh request ID after review
  to use the new deployment policy; a retry with the old ID returns the old record.

Changing the setting never approves historical requests and never changes the
policy of executing records. Execution, restart recovery and rollback recheck
permissions using the saved mode. Signatures, expiry, immutable scope, resource
locks, CSRF, login protection and durable receipts continue to apply.
