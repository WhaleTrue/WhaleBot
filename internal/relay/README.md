# Optional outbound relay

Relay supplies transport capability `mesh.relay`. It is not an orchestrator, identity issuer, storage provider or global membership service. Nodes still exchange signed origin manifests, resolve capabilities locally and authorize business operations at their destinations.

`Register(node, dataDir)` reads comma-separated `MESH_RELAYS`. Empty means this node can accept relay sessions without dialing another hub. A node behind NAT embeds `relay` alongside its ordinary capability packages and sets reachable relay URLs. `RegisterWithOptions` also accepts reconnect interval, default one second, minimum ten milliseconds. Workers are registered with `Node.AddWorker`; shutdown closes sessions and transports. The package has no persistent relay database.

## Connection and request flow

1. The outbound node fetches `/mesh/identity` and verifies the relay's signed trusted manifest.
2. It dials the address with the SDK's normal HTTP/verified HTTPS policy.
3. A signed `POST /relay/connect` requests HTTP/1.1 Upgrade protocol `whalemesh-relay-v1`, carrying `{instance_id,name}`. The hub requires the dialing principal's `mesh.relay` node scope and matches the hello against that principal's announced node identity.
4. Yamux carries normal HTTP streams back down the established outbound connection to the dialing node's `Node.Handler()`.
5. The node advertises `<relay-base>/relay/forward/<principal>/<instance>` alongside any explicitly configured direct addresses.
6. Callers use their ordinary capability binding. The hub restores the original application path/query and verifies the original signed request for its final target, including caller `mesh.relay` scope.
7. The destination verifies that original signature again, then enforces the actual business operation scopes and ownership rules.

The forwarding entry uses `HandlePublic` only to restore signed application routing before authentication. It is not an unauthenticated business API. No caller becomes the relay principal, no credential is reissued, and no relay permission grants permission to invoke an unrelated destination capability. Both hub and destination must trust the caller and its namespace appropriately. The hub must trust the outbound target as well.

A session key is principal plus instance. Reconnecting the same key replaces and closes its old session. The old disconnect cannot delete the newer session. A node restart gets a new instance route; membership expiry and destination target checks prevent the old route from becoming authority for the new process.

The descriptor's primary input schema is the typed hello object. Its output schema is `{"type":"null"}` because successful HTTP 101 has no JSON response body. The streaming operation switches to yamux; it is not a JSON RPC response. The capability retains its `mesh.relay` scope and readiness reflects registration, not tunnel connectivity.

## TLS and network configuration

Relay URLs accept HTTP or HTTPS, including an optional base path, without credentials, query or fragment. HTTP logs an explicit unencrypted-transport warning. Use verified HTTPS across hosts. Ed25519 signs integrity and identity; it does not encrypt bodies or secret values.

`MESH_CA_FILE` adds private CA roots to system trust; hostname verification remains enabled. Native listeners use paired `MESH_TLS_CERT` and `MESH_TLS_KEY`. The outbound upgrade explicitly negotiates HTTP/1.1. A TLS reverse proxy must preserve Upgrade, method, original application path/query, signed headers and request body. Route `/mesh/*`, `/relay/connect` and `/relay/forward/*`. Do not rewrite the signed application data or buffer long streams.

The outbound node marks inbound yamux requests with the TLS state of its verified outer connection. Secret endpoints therefore recognize a TLS-backed relay tunnel. This is not end-to-end encryption from caller to destination: the relay is a trusted transport hop and can observe forwarded plaintext. Protect both caller-to-relay and target-to-relay links. TLS termination/proxy behavior on secret routes must be checked against the deployment's actual endpoint policy; do not set the plaintext-secret exception to disguise an untrusted plaintext hop.

The root `relay` Compose profile publishes loopback HTTP for development, not a public endpoint. `compose.remote.yml` gives a remote node no host port and points both seeds and `MESH_RELAYS` at a reachable relay. Equal Docker network names on two hosts do not connect their networks; container DNS such as `relay:8080` does not work remotely. Use a public/private routed DNS name with matching certificate. See [multi-host runbook](../../docs/multi-host.md).

## Failure and recovery

Handshake budget is ten seconds; TCP dialing is bounded at five seconds. Yamux keepalive and connection write timeout are ten seconds. Hub HTTP transport has a 30-second response-header timeout and idle pool limit of 16 per target connection. The tunneled HTTP server bounds header/body reads and idle time; application calls retain signed deadlines and cancellation.

Reconnect uses exponential backoff from the configured minimum to 30 seconds. Connections that survive more than 30 seconds reset backoff. Logs distinguish connected, disconnected and reconnecting states. A missing outbound session yields HTTP 503 `relay_unavailable`; forwarding transport failure yields HTTP 502 `relay_transport`.

Multiple `MESH_RELAYS` add multiple advertised routes. The SDK deprioritizes recently failed routes on later calls. A relay restart drops in-flight streams, then outbound workers reconnect. It does not replay failed business requests. A transport error during a mutation means outcome may be unknown; inspect destination state before any retry.

Relay readiness describes its registered capability, not a promise every outbound tunnel is currently connected. Membership and reachability can temporarily differ during partition/reconnect. Optional relay loss affects those routes, not unrelated direct mesh calls or provider data.

## Operation

- Grant `provides: ["mesh.relay"]` to nodes advertising this capability, plus their ordinary business advertisements.
- Grant `mesh.peer` for discovery/exchange, `mesh.relay` for connection/forwarding, and only required destination operation scopes.
- Provision separate persistent node keys; never copy one key across independent remote replicas.
- Inspect advertised route principal/instance, hub connection logs and destination membership before changing resolver policy.
- Treat a stopped relay as a route outage, not evidence that a stateful provider should migrate.

The implementation has no automatic relay session quota or bandwidth accounting. Trust grants and protected exposure remain important; this is not an anonymous multi-tenant tunnel service.

Integration owner: root acceptance script exercises an isolated second Docker network and relay restart. That is not proof of physical WAN, NAT, proxy or public-certificate operation. No passing result is claimed here. See [discovery/resolver](../../docs/discovery-resolver.md), [security](../../docs/security.md), and [troubleshooting](../../docs/troubleshooting.md).
