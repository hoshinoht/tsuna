# 02 — Protocol and OpenCode adapter

Status: proposed specification. See [core](01-core.md) for storage invariants.

## 1. Responsibility split

Go owns data operations, validation, indexes and transactional storage. The
OpenCode adapter owns native registration, trusted caller identity, the actual
host's permission engine and invocation cancellation. The Go engine must not
claim to evaluate OpenCode session/organization rules itself.

Retain these native identities through compatibility rollout:

`workplan_create`, `workplan_update`, `workplan_patch`, `workplan_reset`,
`workplan_read`, `workplan_list`, `workplan_inspect`, `workplan_validate`,
`workplan_checkpoint`, `workplan_resume`, `workplan_compact`, `workplan_doctor`,
`workplan_compact_preview`.

Shiori is the project/CLI brand, not permission to rename tools, artifacts or
agents. CLI sketches such as `shiori resume <id>` are proposed, not runnable
commands in this specification-only repository.

## 2. Transport and capabilities

Default candidate: a bounded, versioned JSON-lines protocol over a private local
stdio connection. No unauthenticated network listener, implicit download or
automatic service startup. A long-lived child may cache indexes, but a one-shot
CLI must share the same engine contracts.

Envelope fields:

| Field | Meaning |
| --- | --- |
| `protocolVersion` | Initially 1; incompatible versions fail before mutation |
| `requestId` | Unique per live request; not authority or an approval token |
| `operation` | Known method identity, never arbitrary executable text |
| `input` | Strict operation arguments; unknown native argument keys reject |
| `hostContext` | Broker-owned identity/root data, not model-supplied input |

Response: correlated request ID, result or structured error, and current hashes
where meaningful. stdout is protocol only; diagnostics go to stderr with secret
redaction. Bound frame size and parser nesting; stream deliberate large-content
transfers rather than assuming default line-scanner limits handle large plans.
Numeric precision, duplicate-key handling, exact property names and unknown-field
preservation must be tested across Go and JS.

Handshake advertises core/protocol/schema versions, known operations, storage/hash
algorithms, supported platforms, durability capabilities and optional extensions.
It must distinguish supported capability from configuration intent. Adapter must
not infer write safety merely from a compatible version string.

Initial parser/transport limits are configurable proposals: 16 MiB ordinary
frames, 64 MiB individual artifact with explicit opt-in for larger inputs, and
128 nesting levels. Reject clearly before allocation/write; benchmark and adjust
these limits before freezing release defaults. Bounded resume output is separate.

## 3. Prepare → authorize → commit

1. Core prepares a mutation: exact read/write/delete/lock/staging/archive resources,
   before/after content digests, expected state, operation and root binding.
   Preparation must not create files or locks.
2. Adapter independently validates the intent's project and artifact scope, then
   requests authorization from the executing host for exact canonical resources.
3. Adapter binds the definitive approval to this live invocation and prepared
   intent digest. Core commits only that unchanged intent and rechecks state under
   locks. Changed resources/content/preconditions require new preparation/approval.

The prepared mutation is single-use and expires on cancellation, disconnect,
process/host reload or changed state. Do not accept an input flag such as
`approved: true`, an old preview token or a model-provided actor as authorization.
Use broker-held opaque capabilities on the private transport; any public commit
surface requires authenticated, intent-bound capabilities and its own review.
No permission grants are cached or serialized into journals.

All filesystem mutations, including staging/internal coordination paths, must
be covered by the intent. Reads of linked files must respect the host's read
authority as well; Go must not become a shortcut around sensitive-file policies.
Path scope validation does not replace the permission engine.

## 4. Native host authorization

Verified baseline: OpenCode runtime 2.0.19 with plugin/client 2.0.20. The plugin
permission domain exposes a subset of the public client API; upgrading alone
does not add a plugin-context path-request method. The baseline uses the public
client permission endpoint, not private casts or self-approved replies.

Preserve these requirements:

- Discovery returns a candidate authenticated service, not proof of the actual
  executing host. Never call `Service.ensure` from the adapter.
- Prove this plugin instance and canonical location with a fresh in-memory HMAC
  challenge; prove the authenticated event stream is ready with its correlated
  echo before creating a request. Do not publish the proof secret.
- Filter proof waiters by the fresh challenge before resolving. Filter asked
  waiters by action/session/source/resources/authorization marker/location before
  resolving; later verify returned request ID. Ignore unrelated/replayed events.
- Only definitive allow or a matching genuine user reply permits commitment.
  Deny, rejection, ambiguity, lost stream or abort fails closed. Production code
  never calls permission reply/rule APIs to approve itself.
- Source session, agent, message/tool-call IDs and AbortSignal come only from
  trusted native `ToolContext`, never operation arguments.

Absolute canonical resources follow host whole-path matching semantics; do not
silently translate relative deny rules into a homemade policy evaluator.
Unsupported nested/remote/standalone host bindings fail closed. Supporting them
requires explicit endpoint/auth configuration, same-host proof and real tests;
credentials must not appear in diagnostics, plan files or the Go protocol log.

## 5. Role matrix

| Operation | Native role |
| --- | --- |
| Create/update/patch/reset authoring | `plan` or `orchestrator` |
| Checkpoint, compaction apply, recovery | `orchestrator` only |
| Read/list/inspect/validate/resume/doctor | Caller whose effective tool rules permit it |
| Compact preview | Independently authorized read-only tool |

Role checks supplement host policy; they never widen it. `compact_preview`
omits/forces preview mode and rejects injected apply. Recovery arguments are
mutually exclusive with ordinary updates. Native root overrides and caller/
authorization/fault-injection fields are not accepted from model input.

## 6. Cancellation and lifecycle

Bridge `AbortSignal` to a Go request context and explicit cancellation frame.
Go must propagate `context.Context` through waiting, parsing, hashing and storage;
`context` does not magically interrupt every filesystem syscall or another
process. Check cancellation before and after I/O and every publication boundary.

An approval arriving after cancellation cannot reactivate an invocation.
Cancellation before authorization leaves no filesystem side effects. If an
already-started syscall publishes an artifact while cancellation arrives, stop
further publication, preserve recovery state, and report an uncertain/partial
outcome rather than “nothing changed.” Do not implicitly roll back external edits.

On transport loss, abort all affected requests and stop new commits. Child exit
and host unload must clean listeners/resources; if graceful cancellation fails,
terminate only the owned process and retain durable recovery evidence. Reconnect
requires a new handshake and fresh approval, not replay of outstanding commits.

## 7. Schema and result contracts

Commit versioned machine schemas as the eventual common contract and generate
language bindings/registration descriptions, rather than manually duplicating
Go structs and adapter property maps. Semantic validation remains necessary:
dependencies, artifact scope, field-presence intent and conditional hash/token
requirements cannot be reduced to a shallow shape check.

Migration must prove input acceptance/error-path parity against the completed
Zod baseline. Go's default struct decoding is not enough to guarantee exact
case-sensitive property matching or preserve all unknown JSON metadata.
Preserve omitted/null/value distinctions and unknown stored metadata, while
rejecting unknown native input fields.

Required error classes: invalid input/structure, missing artifact, stale state,
unsupported capability, permission denied/rejected, cancelled, lock unavailable,
ownership conflict, recovery required and external-edit conflict. Report safe
field paths/current hashes/retrieval instructions; never raw credentials.

Standalone CLI authority is explicitly **OS/local operator authority**, not
OpenCode policy enforcement. If an AI invokes it via a shell, the host's shell
authorization governs; do not advertise that as native exact-path policy parity.

## 8. References

- [OpenCode V2 plugins](https://opencode.ai/v2/docs/build/plugins)
- [Public client and discovery](https://opencode.ai/v2/docs/build/client)
- [API](https://opencode.ai/v2/docs/api)
- [Permissions](https://opencode.ai/v2/docs/permissions)
- [Go request cancellation](https://pkg.go.dev/context)
- Next: [performance](03-performance.md), [acceptance](05-migration-and-acceptance.md).
