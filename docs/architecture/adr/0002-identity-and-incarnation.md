# ADR 0002: Separate logical identity, principal, and incarnation

Status: Accepted

## Context

A container name alone cannot distinguish a restart, a replica, and a different authority reusing a name. Capability names are not authorization identities.

## Decision

A node manifest carries namespace, logical name, instance ID, host ID, principal, labels, and annotations. The SDK creates a new instance ID for each process incarnation. Principal identifies the persistent signing key. Namespace defines the trust boundary. Host ID expresses placement, not authorization.

Membership announcements contain the complete instance identity, an origin-incremented sequence and an Ed25519 signature. Watermarks are keyed by the full instance identity, never by a remote wall clock. Duplicate or lower sequences do not refresh local liveness. Restarted instances join independently; older instances expire unless they continue originating progress. This also permits simultaneous same-name instances.

## Consequences

Node keys survive restarts in private identity volumes; incarnation IDs do not. Replicas need distinct provisioned identities. Labels and host claims remain untrusted placement information until the manifest signature and trust grant pass validation. Persistent watermarks reduce replay after receiver restarts; restoring older state can weaken that protection and requires operational care.
