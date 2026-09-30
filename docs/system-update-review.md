# System update review

## Capability inventory

| Capability | Public implementation | Disposition |
| --- | --- | --- |
| Authentication, captcha, sessions, permissions | Existing authentication and authorization modules | Retained; no weakening of login or authorization |
| Operation confirmation, independent approval, audit | Existing confirmation and task/publication handlers | Retained; image preparation does not authorize execution |
| Trusted build registration and immutable identity | Release manifests, trust keys and registration worker | Retained; no unsigned or tag-based update path |
| Distribution, resume and attempt history | Distribution, registry cache and transfer modules | Retained; direct pull remains supported |
| Protected cleanup | Cleanup preview/approval and Agent managed-resource checks | Retained; no global prune |
| Recovery and environment persistence | Agent recovery-unit and environment journals | Retained; example now explicitly enables selected-image persistence |
| Update navigation and live result | Shared publication UI | Added dedicated System update entry, inventory-only read and bounded record observation |
| Build time presentation | Existing nullable build metadata | Current provenance now exposes build time; unknown stays unknown |
| Application-specific operations and deployment data | Outside public contract | Excluded |

There is no new execution queue, schema migration, dependency or approval bypass.
The existing public implementation covers the reviewed generic capability set;
this change integrates it into a self-update workflow rather than copying another
application's state machine. Detailed source-to-target mappings are kept outside
the public repository.

## Validation boundaries

See the dated execution entry in [verification](verification.md). Docker fixtures
use synthetic services and disposable databases; they do not constitute production
migration, live registry/SMTP validation or browser visual acceptance. Signed
manifests have no schema compatibility range; operators must follow migration notes.
