# WhaleMesh

**Capability-Based Autonomous Container Mesh**

Everything is Docker. Every Docker is an Equal Mesh Node. The Architecture Emerges from Capabilities.

[中文](README.md) · [Architecture](docs/architecture/overview.md) · [Schemas](docs/architecture/schema-contracts.md) · [Capability development](docs/developer-guide.md)

**Branches:** `dev` retains pre-rewrite WhaleBot (`e71dbdf`); `v3` contains the complete WhaleMesh rewrite and all subsequent development. These versions are incompatible; do not merge the rewrite back into `dev`.

WhaleMesh replaces WhaleBot `dev`'s centralized registration, proxy chain and special UserDocker protocol. Every business container embeds the same node protocol, owns a local verified membership/capability view, and resolves dependencies by capability rather than a fixed service role. There is no mandatory central orchestrator, membership database, Executor, Relay or dashboard.

Protocol equality does not imply equal host privileges. Only an explicitly authorized Executor can access Docker Engine. Advertising `container.manage` cannot grant Engine access or administrator rights.

## Start

Requirements: Docker Engine and Compose v2.24+ or v5. No external API key is needed for the explicit development provider.

```sh
docker compose up -d --build
docker compose exec web cat /identity/admin-password
```

Open `http://localhost:18000`. Username defaults to `admin`; the password is randomly generated and persisted. Each node has an independent Ed25519 private key. There is no shared administrator key or fixed default password. An existing `.env` can override the port; use `docker compose port web 8080` to discover the actual binding.

`LLM_MOCK=1` explicitly returns labeled deterministic development output, not real inference. Configure actual OpenAI-compatible model profiles and disable the mock for real model access. Profiles describe cost class and tool/streaming support. Automatic unspecified model selection is restricted to providers explicitly marked free. Paid or unknown-cost models require explicit model/provider choice; failures never silently turn into mock output or a different paid account.

The default dashboard port binds loopback only. Development bridge traffic is signed HTTP, not encrypted. Compose explicitly enables plaintext secret transport only for that isolated trusted bridge and logs a warning. Routed/multi-host deployments require verified HTTPS and must disable that exception.

## Minimal chat without dashboard or Executor

```sh
docker compose -f compose.minimal.yml up -d --build
docker compose -f compose.minimal.yml run --rm chat 'Hello WhaleMesh'
```

The terminal adapter resolves `agent.run`; the agent resolves session and model capabilities. Docker DNS supplies bootstrap reachability only. Discovery and resolution do not depend on an orchestrator or Docker Socket.

## Optional deployments

```sh
docker compose --profile multi-provider up -d
docker compose --profile telegram up -d
docker compose --profile executor up -d
docker compose --profile example build hello
docker compose --profile example up -d hello
docker compose --profile relay up -d
```

Only Executor mounts the socket. Telegram requires a real token and whitelist; without them it reports an unconfigured state. The example image is an ordinary SDK provider, not a special workload protocol. Remote nodes use `compose.remote.yml`, a separately provisioned identity and an outbound relay connection.

## Design

- Persistent principals, logical names, hosts and process instances are distinct identities.
- Strict versioned Node, Capability and Deployment JSON Schemas describe protocol contracts. Public manifests contain no credentials.
- Authenticated peer exchange distributes signed origin sequences. Receiver-local TTL and persisted per-instance watermarks suppress stale/duplicate progress without comparing remote clocks.
- Each local resolver checks interface/version, readiness, namespace, permissions, attributes, pins and selection policy. Deterministic choices include explanations.
- Business capabilities retain typed HTTP operations. The common layer handles authentication, authorization, deadlines, signed trace/request context, cancellation, streaming and structured errors.
- Explicit Tool Export policies control what reaches the model. Model arguments cannot grant identity, Docker authority or user confirmation.
- Stateful bindings preserve verified principal and persistent data boundary across process restarts. Rebinding to unrelated data is never implicit migration.
- Optional outbound yamux relays extend transport reachability only. Destinations verify original request authority independently.

## Product surfaces

The agent implements bounded ReAct, tool calls, durable session history, skills/memory retrieval, secret references and redaction, trusted approvals, structured traces, usage and benchmarks over the SDK. Model providers support independent replicas, multiple persisted profiles, actual OpenAI-compatible requests, tool calls, streaming and timeouts. Inference is never automatically replayed, including HTTP 429, transport failures and partial streams.

Persistent capabilities cover sessions, notes/search, AES-GCM encrypted secrets, versioned skills/ZIP import, confined workspace/artifacts, events and statistics. The Svelte dashboard follows [xianii-theme](https://github.com/Nigh/xianii-theme) and opens on a tiled Nodes page. Each tile opens that exact node's available model/adapter settings or advertised operation console. Topology presents node-focused capabilities, dependencies and bindings vertically, with expandable candidates and no horizontal scrolling. Chat, sessions and capability-based management remain available. Desktop controls and spacing take priority; mobile retains basic usability. Telegram retains commands, session rotation, progress, errors and artifact delivery.

Executor interprets authorized desired deployments and controls only its labeled resources. Dynamic containers use normal mesh identities from constrained, preprovisioned pools. Default policy rejects host binds, socket exposure, privilege escalation, dangerous networks and unbounded resources. External images require immutable digests and trusted administrator signatures, not request approval booleans.

## Source and verification

```text
cmd/                 node, bootstrap, terminal chat
pkg/mesh/            role-free SDK
schemas/             strict JSON contracts
internal/agent/      agent and benchmark
internal/llm/        model provider
internal/providers/  durable business capabilities
internal/executor/   Engine authorization boundary
internal/relay/      optional outbound transport
internal/web/        browser authentication and SDK client
internal/telegram/   IM adapter
web/                 Svelte UI
examples/hello/      independent capability provider
scripts/             real Docker acceptance
deployment/          identity plans and deployment examples
```

Source development uses Go 1.26 and Node 24. Commands below are runnable checks, not a claim that every environment has passed them.

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
cd web && npm ci && npm run build && node --test src/api.test.js src/topology.test.js
cd ..
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
python3 scripts/acceptance.py
```

Acceptance owns a separate Compose project and isolated remote network. Real Docker tests exercise no-orchestrator chat, dynamic knowledge, provider coexistence/failure, managed nodes, authorization and relay recovery. Actual external model quality and Telegram delivery need real credentials; mock success is not evidence of those integrations.

## Documentation

- [Single-host deployment](docs/single-host.md)
- [Multi-host and Relay](docs/multi-host.md)
- [Security](docs/security.md)
- [Discovery and resolver](docs/discovery-resolver.md)
- [Schema contracts](docs/architecture/schema-contracts.md)
- [Capability development](docs/developer-guide.md) and [Hello](examples/hello/README.md)
- [Executor](internal/executor/README.md)
- [Batch model benchmarks and score history](docs/benchmark-automation.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Legacy implementation audit](docs/architecture/legacy-audit.md) and [ADRs](docs/architecture/adr/)

This is a clean breaking cutover. Old APIs, registration clients and database formats are not retained. Old user volumes are neither silently migrated nor removed. Keep AGENTS and affected component/runbook documentation aligned. Query graphify before raw source exploration; run `graphify update .` after code changes.

MIT License; see [LICENSE](LICENSE).
