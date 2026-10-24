# ADR 0005: Use signed anti-entropy membership and explicit lifecycle

Status: Accepted

## Context

Transport connectivity does not imply business readiness. A crashed provider must eventually disappear without requiring a central registrar to delete it.

## Decision

Nodes exchange signed manifest snapshots with seeds and learned addresses. Receivers verify trust, namespace, advertisement grants, signature and per-instance origin sequence before admitting a peer. A strictly newer sequence starts a receiver-local monotonic TTL; forwarding the same announcement never extends it. Membership ordering does not compare system clocks across machines. Previously learned addresses remain reconnect targets so partitions can heal without a live seed.

Manifest lifecycle distinguishes alive, ready, and draining. Capability readiness is also explicit. A closing node advertises draining before stopping its transports. Readiness depends on required bindings rather than only a successful TCP listener.

## Consequences

Discovery converges eventually; it does not provide consensus or exactly-once events. A provider can become unavailable between resolution and invocation. Consumers must return a structured error rather than infer availability from stale membership. TTL and exchange interval trade outage detection speed against traffic. State cannot remain available through an indefinitely disconnected partition unless it is provided locally.

Watermarks checkpoint every two seconds outside the registry lock and flush at graceful close. An abrupt crash can lose one interval plus I/O latency; failed disk writes enlarge the window and are reported. History retains up to 65,536 incarnations, reserved per principal, with at most 16 live incarnations per principal. Snapshots rotate at 256 records and 8 MiB, rather than growing beyond transport limits. A receiver lacking recent history can briefly admit an old announcement, but it expires unless its origin advances. Target-bound authentication prevents that cache replay from authorizing an invocation against a new process instance. Signed request freshness uses clocks separately from membership liveness.
