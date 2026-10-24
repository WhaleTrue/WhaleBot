# ADR 0004: Use explicit per-principal signing grants

Status: Accepted

## Context

A shared node token allows any holder to impersonate another node. A valid signature proves key possession but does not authorize a capability advertisement or business operation.

## Decision

Bootstrap provisions an independent Ed25519 key for each principal. Private keys stay in per-node identity volumes. Public trust grants bind principal, public key, namespace, invocation scopes, and permitted capability advertisements (`provides`). An empty advertisement grant denies advertisements.

Nodes sign membership announcements and HTTP requests. Request authentication binds the intended target and application request, including signed body data. Providers authorize operation scopes against the configured grant, not against caller-supplied manifest permissions. Browser user identity is separate from node principal; delegated user context must remain authenticated through the forwarding path.

## Consequences

Provisioning is a deployment dependency, not an online control plane. Key loss requires explicit reprovisioning. Scope grants and advertisement grants must both be configured. TLS is needed where transport confidentiality matters: signatures authenticate requests but do not encrypt their contents. Public liveness does not expose protected business APIs.
