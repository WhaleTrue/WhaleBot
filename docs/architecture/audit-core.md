# Core and relay implementation audit

## Evidence

Graph query `graphify query "registry registerclient nodes tunnel discovery authentication relay"` identifies the registry, registration client, node hub, and outbound tunnel. The implementation was then read directly.

- `orchestrator/internal/registry/registry.go:17-208` stores components by mutable name, accepts unsigned registration, derives heartbeat TTL locally, and chooses the first matching map entry. Its type-specific selectors encode deployment roles. Tunnel presence bypasses heartbeat expiry. There is no incarnation, monotonic sequence, trust boundary, dependency resolution, or partition reconciliation.
- `stats/internal/registerclient/registerclient.go:1-130` sends unsigned registrations to one orchestrator. An identical client is copied across services. Startup retries, but topology never exchanges between peers. A failed central endpoint prevents discovery.
- `orchestrator/internal/nodes/hub.go:27-193` accepts a shared token, upgrades HTTP to yamux, and installs one reverse HTTP client per named node. Reconnection replaces the previous named session. The token does not distinguish principals or limit namespaces and scopes. The node hub belongs to the central orchestrator.
- `user-docker-manager/internal/tunnel/tunnel.go:25-145` provides a working outbound yamux connection with reconnect and buffered upgrade reads. It serves ordinary HTTP over multiplexed streams. It lacks per-principal authentication and uses one designated orchestrator.

## Cutover

The new SDK uses ordinary capabilities rather than role enums. Every node owns authenticated HTTP routes, a signed membership view, deterministic dependency resolution, and bounded clients. Trust grants bind Ed25519 principals to namespaces and scopes. A new random incarnation distinguishes each process; signed monotonic announcements expire rather than being refreshed by repeated gossip. Seed and discovered addresses remain retry targets after expiry, allowing partition recovery without central registration.

Relay remains optional. A relay provider accepts authenticated outbound yamux sessions from any trusted node and forwards signed requests to the original node HTTP handler. Relays are interchangeable transport peers, not schedulers or registries. A disconnected session is removed conditionally so an old connection cannot erase its replacement. Application authorization remains at the destination.

Legacy directories are removed by the parent cutover owner. Pre-existing deleted server files are not restored.
