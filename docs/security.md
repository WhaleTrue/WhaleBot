# Security model

## Identities and trust

A mesh principal is an independently provisioned Ed25519 key, not a container name, label, browser user or Telegram user. The private key persists in that node's private identity volume. Every process incarnation gets a fresh instance ID. Multiple principals can use the same logical name without overwriting each other.

Each node loads an administrator-owned public trust map. A grant binds public key and namespace to two independent allowlists:

- `scopes`: operations the principal may invoke.
- `provides`: capability IDs the principal may advertise.

Empty `provides` grants no provider authority. Wildcards are explicit administrator choices, not defaults for third-party nodes. A normal discovery peer cannot become a secret store or Executor by modifying its manifest. A signed manifest can claim only scopes already granted to its principal. Host IDs and labels describe placement; they are not permission proofs.

The one-shot deployment bootstrap creates separate private key files, copies only public trust to peers, and generates a random initial browser password. It has no online membership or business API. Adding trust requires an explicit administrator plan change and trust consumers restarting; discovery is not certificate issuance.

## Request authentication

Cross-node HTTP requests bind method, application path/query, canonical full target identity, authenticated namespace/principal, timestamp, deadline, one-use nonce, body hash, content type, delegated owner and trace/request IDs under an Ed25519 signature. `IdentityKey` escapes each namespace/principal/logical-name/instance component, preventing URI delimiter collisions. A relay preserves the original application signature; destination verifies it again. Redirects do not forward signed credentials. Handler-level scopes remain the final execution boundary.

A bounded, per-principal reserved nonce cache rejects replay. A restarted destination has a new target instance, so a request for its previous instance is not valid there. Only identity/exchange bootstrap routes accept the discovery target. Signed HTTP authenticates request integrity; it does not encrypt content or independently sign response bodies. Production TLS and trusted relay hops protect responses. Node clocks need reasonable synchronization for request freshness/deadline checks. Membership ordering and liveness do not compare clocks from different nodes.

TLS supports `MESH_TLS_CERT`, `MESH_TLS_KEY` and an optional `MESH_CA_FILE`. Certificate verification is never disabled by the SDK. Remote relays require HTTPS. Reverse proxies must preserve the original path/query and relay Upgrade behavior.

Root Compose is an explicitly local development deployment. Its dedicated trusted bridge uses signed HTTP and explicitly sets `MESH_ALLOW_PLAINTEXT_SENSITIVE=1`, which emits a warning. Otherwise a capability or operation with `transport_security: tls` requires HTTPS at the client and actual TLS at the handler. Secret operations and credential-bearing configuration writes declare this trait; the SDK never infers it from business capability names. Do not enable the exception on an untrusted bridge, routed network or public host. Relay TLS is hop-by-hop: a selected relay is trusted with forwarded plaintext, not an end-to-end encryption boundary.

Authenticated invocation admission is bounded at 64 concurrent requests per node and 32 per principal. Saturation returns HTTP 429 before business execution. Replay-cache space is reserved per configured principal; membership history has separate principal quotas. These bounds do not replace host/network isolation or authorization of capability publishers.

## Users, adapters and tools

Browser sessions use bcrypt passwords, random server-side session IDs, HttpOnly/SameSite cookies, expiry, same-origin checks and CSRF tokens. Browser users have their own operation scopes. The generic browser client validates the selected advertised operation and all of its scopes; the web node's broader grant is not copied to users. Users cannot choose arbitrary provider URLs, substitute owner identity or read raw secret resolution output.

Trusted adapters/runtime may forward a verified owner only with the corresponding `*.delegate` scope. Ownership is in signed request data or signed transport context. Storage includes namespace and effective owner in record keys. Telegram allowlists are independent from node trust. An IM user is never accepted as a mesh principal.

Runtime only exports explicit, trusted `ToolExport` declarations. Exported schema, operation mapping, required scopes and side effects are validated. Unknown tool names do not reach arbitrary handlers. Confirmation records bind exact canonical arguments, owner, session and expiry; model-generated confirmation fields cannot create such records. Executor approvals are an additional independent policy boundary.

## Secrets and persistence

Secret plaintext is absent from manifests, peer exchange, topology, model input, configuration reads and browser resolution responses. AES-256-GCM associated data binds ciphertext to namespace, owner and key. Existing encrypted data without its original key causes startup failure, not key regeneration or deletion. Notes, session history, skill content and filesystem artifacts are user data; secure their persistent volumes and backups as well.

Runtime resolves secret references only at the trusted execution boundary and redacts known values from results, replies and traces. Redaction is defense in depth, not a substitute for operation authorization. Do not use secrets as public labels, model IDs or capability attributes.

## Docker boundary

Only Executor mounts Docker Engine. Every managed resource must have Executor, namespace, owner and deployment labels. Even an administrator cannot use this API to control unrelated unmanaged containers. Ordinary nodes cannot obtain Engine rights by declaring a capability.

Deployment is desired state, not authorization. Default policy rejects host binds, socket mounts, privileged containers, dangerous networks and unbounded resources. Workspace helpers use `os.Root` confinement and bounded binary transport. Created nodes use normal mesh identities from an explicitly provisioned constrained pool; creation does not mint new trust.

Externally supplied images require an immutable digest and an administrator-signed `image.use` approval bound to subject, namespace, image and expiry. A request boolean is never proof of approval. Browser administrators can issue approvals through the authenticated approval endpoint; the signer key is not sent to the browser. Treat Engine and approval-key access as host-level privilege.

## Operational boundaries

Trust files are local configuration, not a distributed revocation service. For immediate compromise response remove the grant, restart consumers, terminate compromised workloads and rotate affected application secrets. Backups can contain private keys, encrypted values and user data. Restoring old membership/nonce state has replay implications; preserve fresh target incarnations.

Membership history retains origin-sequence watermarks to reject old announcements, with a hard 65,536-incarnation ceiling per local history. This is a deliberate bounded-memory protection; long-lived fleets reaching that ceiling need explicit history compaction or an administrator reset, not silent eviction that weakens replay suppression. A previously unknown stale announcement may be admitted once and then expires without new origin progress; authenticated forwarding cannot keep a repeated sequence live.

Real external model and Telegram transport verification requires operator credentials. Mock-provider success does not certify an external account, API quota, model quality or IM delivery.
