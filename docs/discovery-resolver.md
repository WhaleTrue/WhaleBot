# Discovery and capability resolution

WhaleMesh has no registration server or central routing table. Each `mesh.Node` owns its trusted membership view, dependency graph and resolver state. Compose service names select deployment bundles, not protocol roles.

## Identity and trust

A principal uses a persistent Ed25519 key. Every process start generates a fresh 32-character `instance_id`. `IdentityKey` is `namespace/principal/name/instance_id` with URI-bearing components escaped independently; a bare instance or logical name is not authority. `host_id` only describes locality.

Each trust entry binds a public key to one namespace and two independent permission sets:

- `scopes`: operations the principal may invoke.
- `provides`: capabilities the principal may advertise.

Both support exact names, `*` and trailing `.*` prefixes. A provider's advertised security permissions cannot exceed its trust grant. Trust files are deployment inputs, not learned through gossip. Restart nodes after changing their files. Limits are 4096 trusted principals and 16 bootstrap seeds per node.

## Origin announcements and local expiry

A signed announcement contains `manifest`, positive `sequence` and `signature`. Its signature covers the manifest and sequence with the `whalemesh.membership/v1` domain separator. There is **no signed membership timestamp or expiry**.

The origin increments its sequence when creating a snapshot or signed identity response. Forwarders retain that origin signature and sequence. Receivers accept only a higher sequence for the same membership identity. Equal or older sequences do not refresh liveness. Therefore, repeated gossip of a dead node's last announcement cannot keep it alive.

On acceptance, the receiver records its own local `LastSeen` and deadline `now + TTL`. Resolution immediately ignores expired entries; periodic expiry also removes them and emits an `expired` event. Graceful shutdown first announces `draining=true, ready=false`, bounded by two seconds and the shutdown deadline. Draining nodes are never candidates.

Replay watermarks persist in `MESH_DATA/membership.json`. Expiring a peer does not erase its watermark. Total incarnation history is capped at 65536 entries. Each principal receives at most `floor(65536 / number_of_trusted_principals)` entries. A new incarnation is rejected once that principal reaches its share. At most 16 live, non-draining incarnations of one principal are accepted concurrently. Provision separate keys for independent replicas; do not treat these bounds as a reason to share keys.

Checkpointing runs every two seconds outside the registry mutex and flushes on graceful close. Successful I/O bounds crash loss to one interval plus write latency. Disk errors retain dirty state and emit an error; they enlarge that window. A replay admitted after lost cache progress still expires locally and cannot authenticate as a restarted destination instance. This cache is not a transactional business store.

A newly accepted announcement replaces the discovery target addresses for that principal. The node keeps the latest advertised address list, not an unbounded history of old URLs. Targets remain available for partition recovery after membership expiry. Bootstrap seeds remain configured recovery contacts.

## Bounded peer exchange

`POST /mesh/exchange` requires `mesh.peer` and exchanges at most 256 signed announcements. `GET /mesh/identity` and `GET /.well-known/whalemesh` require the same scope. Outbound snapshots rotate under an 8 MiB budget, below the SDK's 16 MiB HTTP body limit; large records cannot permanently starve later records. Receiving several members is not transactional. Canceled exchanges stop processing further records.

Each exchange round:

1. Takes one signed local snapshot, including locally unexpired peers.
2. Selects one address per known principal, plus configured seed contacts.
3. Removes self addresses and duplicate selected URLs, then sorts URLs.
4. Visits at most eight targets, rotating its cursor between rounds.
5. Runs those calls concurrently under one shared deadline.

The round budget is `min(max(interval, 250ms), 5s, TTL/3)`. One dead address cannot consume a full timeout per advertised route. The floor allows schema-bearing announcements to complete without creating a tight cancel/retry loop. This is bounded eventual exchange, not a guaranteed convergence deadline for arbitrary mesh sizes.

Defaults are `MESH_INTERVAL=2s` and SDK `MESH_TTL=15s`. Valid configuration requires interval at least 10 ms and `3 * interval <= TTL <= 24h`. Seeds and advertisements must use HTTP or HTTPS without credentials, query or fragment. Container DNS names work only where that Docker network resolves them.

The transport prefers the first address without a recent failure. Failed routes are deprioritized for 30 seconds; if all failed, the least recently failed route is retried. Calls do not automatically replay a mutation on another address.

## Eligibility before selection

`Node.Resolve(mesh.Requirement)` applies policy overrides by requirement ID, then filters candidates. A candidate must satisfy:

- Same namespace as requester; cross-namespace resolution fails.
- Alive, not draining, unexpired membership, and ready capability. Remote nodes must also advertise node readiness.
- Matching capability ID, interface, compatible version and requested attributes.
- Matching locality and optional pin.
- Requester's trusted invocation scopes.
- A recursively satisfiable graph of required dependencies; required cycles fail. Optional dependencies do not block readiness.

Requirements with explicit `scopes` require **all** those scopes. Without explicit scopes, a capability with scopes requires at least one permitted capability scope. A capability's scope list is the union of its operations, not a demand that read-only callers hold every write/administrator permission. The SDK checks matched operation scopes before sending. The destination always checks its actual handler scopes.

`optional=true` returns an empty binding list rather than an error when no provider matches. Missing non-optional dependencies make node readiness false; public `GET /healthz` reports liveness only. Dependency readiness is not proof that an upstream model account or external service works.

Locality values are `any`, `prefer-local`, `node`/`same-node`, `host`/`local`/`same-host`, and `remote`. Same-host requires a nonempty matching `host_id`. These filters are not authorization controls.

## Selection policies

Candidates first sort by full membership identity and capability ID. Selection is deterministic except where the configured schedule changes between calls:

- Empty or `default`: `prefer-local`, ranking same instance, same host, then remote.
- `prefer-local`, `local-first`, `local-preferred`: locality ranking with stable identity ties.
- `first`, `first-healthy`, `failover`: first eligible candidate. These names do not add an execution retry loop or active health probe.
- `priority`: descending numeric capability `priority` attribute.
- `least-load`, `least-loaded`: advertised numeric `load` plus this node's active calls.
- `round-robin`: rotates eligible candidates per local requirement.
- `random`: deterministic hash of local instance, requirement key and selection counter; not security randomness.
- `consistent-hash`: stable hash of requester principal and requirement key over the current ordered candidates.
- `weighted`: deterministic weighted schedule using nonnegative `weight` attributes, default 1. No positive weight is an error.
- `explicit`, `pinned`: requires `pin`. Pins match a logical name, trusted principal or canonical full `IdentityKey`, never a bare instance ID.
- `broadcast`, `all`, or `many=true`: returns all candidates; does not itself send any request.
- `sticky`: persistent single-provider binding, described below.

`Binding.reason` explains policy, namespace, interface, version, locality, trusted principal and dependency readiness. `Bindings()` reports recorded local resolutions, not active traffic or a global assignment database. `RecordedBindings()` groups those records by `requirement_key`.

`Node.Explain(requirement) (Explanation, error)` previews the next selection using the same filtering and selection path as `Resolve`. It never advances round-robin/random/weighted schedules, creates sticky pins, changes recorded bindings or saves resolver state. Readiness evaluation has the same no-write guarantee. Selection errors are returned alongside the explanation, whose `error` field preserves the message for JSON consumers.

`Node.Candidates` enumerates eligibility without scheduling, pinning or recording. Consumers may inspect provider catalogs, then call `Resolve` with an exact canonical `providers` allowlist. Runtime uses this two-phase path so model filtering still respects the selected routing policy. Stable dependency IDs preserve sticky boundaries across eligibility changes. A capability provided by the requesting node satisfies a local dependency; that local edge is not a cross-node required cycle.

The JSON contract contains `requirement_key`, effective `requirement`, effective `policy`, `sticky`, optional saved `sticky_pin`, `rationale`, `selected`, `providers` and optional `error`. Providers contain `node` identity, `capability`, `eligible`, `selected`, one-based eligible `rank`, `locality_rank`, `priority`, `load`, `weight`, and `reasons`. Reasons have stable `code`, readable `message`, optional `requirement_key` and nested `providers` for failed required dependencies. Codes distinguish namespace, lifecycle/readiness, capability/interface/version, attributes, locality, pin, requester trust/scopes, `required_dependency`, and `required_cycle`; eligible providers report `selected` or `eligible_not_selected`. Deterministic identity order breaks policy ties. Evidence uses this node's unexpired trusted membership view, not remote resolver policies or remote recorded bindings. A valid alternative remains eligible even when another provider has a required cycle.

## Stateful boundaries and deliberate migration

Sticky selection activates through a sticky requirement/policy or provider attributes `stateful=true` or `sticky=required`. It cannot broadcast.

When a capability advertises `state_id`, its persistent binding key is `namespace/principal/name/state_id/capability`. A restart with the same principal, logical name and data can therefore recover its binding despite a new instance ID. Without `state_id`, the binding includes the instance ID.

`MESH_DATA/resolver.json` stores sticky keys and explicit policy overrides. A different principal, provider name or state ID is a different boundary. If the saved boundary is absent, resolution fails with `automatic stateful migration denied`; another available store is not a substitute. There is no replication, merge or data migration protocol hidden behind failover.

`Node.SetPolicy(requirementID, policy, pin)` only accepts IDs declared in that node's manifest. It clears that requirement's sticky key and persists the override. Browser `PUT /api/policy` requires user scope `mesh.policy` and changes only the web node's requirements, not every node's policies. Changing a binding authorizes new selection; it does not copy old data. For dynamically created runtime requirements, restore the original boundary or perform a controlled, backed-up resolver-state change while its consumer is stopped. See [troubleshooting](troubleshooting.md).

## Node scopes are not browser scopes

Node calls use signed principal grants. Browser accounts use cookie sessions and their own scopes. `/api/call` requires every scope on the exact advertised operation, rejects missing operation scopes, validates method/path and resolves a provider through the SDK. The web node must separately hold the destination's node scopes and any delegation scopes. A browser user's scope does not enlarge its backend's node grant, and a broad backend grant does not enlarge the browser user's scopes.

User ownership is injected by trusted adapters. Model arguments and caller-supplied approval flags are not identity or authority. Raw secret resolution is blocked by the browser backend even for administrators.

## Inspection

Authenticated node endpoints `GET /mesh/manifest`, `/mesh/peers` and `/mesh/status` require `mesh.inspect`. Status includes local manifest, unexpired peers, recorded bindings, request/failure counters and dropped topology event count. Browser `GET /api/topology` requires user scope `mesh.inspect` and adds read-only next-selection explanations plus requirement-keyed `recorded_bindings` and supported `policies`. Preview edges and recorded local edges are distinct; remote assignments are not observed. Relay capabilities alone are not route evidence: only actual advertised relay forwarding endpoints can produce relay route edges. Unsigned curl requests to node endpoints are expected to fail.

Topology events are bounded, best-effort broadcasts, not a durable audit log. `Node.Events(ctx)` gives each of at most 32 subscribers an independent 64-event buffer and closes on cancellation or node shutdown. Slow readers drop events and must resynchronize through `GET /mesh/status`; authenticated `GET /mesh/events` exposes SSE. `mesh.inspect` is advertised only when its publishing grant permits it. Track trace/request IDs for operation failures. See [security](security.md), [multi-host transport](multi-host.md), and [architecture overview](architecture/overview.md).
