# AGENT Context (WhaleMesh)

## Source of truth

1. Root Compose files and `deployment/identities.json`.
2. `.env.example` and `pkg/mesh/config.go`.
3. `docs/architecture/overview.md`, schema/SDK code, component READMEs.
4. Root Chinese and English READMEs.

This is the Big Bang rewrite of WhaleBot `dev` as a capability-based autonomous Docker mesh. No old wire/API/database compatibility is intended. Do not recreate a central orchestrator, registration client, role enum or special UserDocker identity. Deployment bundle names are not protocol roles. A new capability must require no core role list or central routing-table change.

## Repository map

- `pkg/mesh`: shared types, strict schemas, persistent Ed25519 identity, authenticated peer exchange, local membership, deterministic resolver, HTTP/SSE clients/servers, lifecycle and topology events.
- `schemas`: embedded draft-2020-12 Node/Capability/Deployment contracts.
- `internal/agent`: ordinary agent execution, trusted exported tools/approvals, retrieval, traces and benchmark.
- `internal/llm`: persisted OpenAI-compatible model profiles and explicit development mock.
- `internal/providers`: consolidated durable session, memory/search, encrypted secrets, skills, confined workspace/artifacts, optional events/statistics.
- `internal/executor`: the only Engine client and privileged deployment boundary. Controls only namespace/owner resources labeled with its public-key fingerprint. Deployment has a minimal `node` template, never a running manifest.
- `internal/relay`: optional authenticated outbound yamux transport; destination retains authorization.
- `internal/web`, `web`: cookie-authenticated user backend and Svelte 5/Vite 8 dashboard/chat/topology. Provider selectors reflect actual providers; model profiles retain explicit cost/features and permit no active model.
- `internal/telegram`: optional outbound Telegram adapter with independent user/session identity.
- `cmd/node`: embeds deployment-selected capability bundles; no business capability selectors in SDK.
- `cmd/bootstrap`: one-shot separate key provisioning, not an online mesh service.
- `cmd/chat`: terminal adapter; `examples/hello`: independent SDK provider.
- `scripts/acceptance.py`: real Docker acceptance, isolated remote network and relay restart.

## Deployment

```sh
docker compose up -d --build
docker compose exec web cat /identity/admin-password
```

Dashboard: loopback `http://localhost:18000`, username `admin`, random persisted password. `LLM_MOCK=1` explicitly labels deterministic development output; no inference is faked on upstream errors. Real model profiles and Telegram credentials require actual configuration. Never log credentials.

Minimal chat, without dashboard/Executor:

```sh
docker compose -f compose.minimal.yml up -d --build
docker compose -f compose.minimal.yml run --rm chat 'Hello WhaleMesh'
```

Root optional profiles: `executor`, `relay`, `telegram`, `multi-provider`, `example`, `cli`. Default dedicated network `whalemesh_net`. Only Executor mounts Docker socket. Remote example `compose.remote.yml` uses a separate identity bundle and outbound relay, not cross-host Docker DNS.

## Security invariants

- Principal identity persists in private Ed25519 files; each restart gets a fresh instance ID. Logical names are not authority.
- Trust grants bind public key and namespace. `scopes` controls invocation; `provides` independently controls advertised capabilities.
- Every cross-node request is signed and full-target/path/body/deadline/nonce-bound. Use escaped `mesh.IdentityKey`, never concatenate URI-bearing principals or select by a bare instance ID. Server verifies authorization at the actual operation.
- Operation scope descriptors must match handler scopes. Capability scopes are the union, not a demand that every caller have every administrative scope.
- Browser and IM users are not node principals. Verified user ownership is delegated only by explicitly trusted scopes; never trust model-supplied identity/approval booleans.
- Raw secrets never enter manifests/topology/browser resolution responses. AES-GCM associated data includes owner and namespace.
- Signed HTTP is not encryption. `transport_security: tls` applies generically to capabilities/operations. Root Compose explicitly enables `MESH_ALLOW_PLAINTEXT_SENSITIVE=1` only on its trusted development bridge. Cross-host uses verified HTTPS; selected relays see forwarded plaintext. `HostID` never proves authorization.
- Stateful bindings follow verified principal plus persistent state boundary. A restarted instance can recover the same data; a different provider/state is not automatic migration.
- Executor cannot control unmanaged resources. External images need immutable digest and trusted signed administrator approvals. Ordinary nodes never get Docker socket access.
- Membership uses origin sequence plus receiver-local TTL, principal quotas and rotating 256-record/8 MiB snapshots. Watermarks checkpoint outside registry locks every 2s and flush on close; abrupt crashes/disk errors can lose recent cache progress.
- Resolver inspection is read-only. `Candidates` plus exact `providers` allowlist supports catalog filtering before a real policy choice. Local self-provided capabilities are not dependency cycles. Keep stable requirement IDs for stateful data.
- Model runs freeze provider/profile/catalog revision. Unspecified selection allows only explicit free models; provider enforces `free_only` and revision before inference. Configuration changes fail instead of silently changing cost/model mid-run.
- Executor runs UID 10001 with socket supplementary `DOCKER_GID`; owned-volume initializer alone uses CHOWN/FOWNER. Workloads remain UID 65532, read-only root and no added capabilities.

## Development and verification

Go 1.26, Node 24, Docker Compose v2.24+ or v5. Use the canonical root module `github.com/WhaleTrue/WhaleBot`; WhaleMesh is the product name, not a separate GitHub repository. Reuse SDK clients and strict JSON helpers; keep network operations bounded and cancellable.

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
cd web && npm ci && npm run build && node --test src/api.test.js src/topology.test.js
# From repository root:
python scripts/acceptance.py
```

Do not claim a command passed unless its output was observed. Real upstream/Telegram verification needs external credentials; mock tests are not proof of those integrations. Browser changes require actual browser verification, not just frontend compilation.

## Graphify (mandatory)

Before raw source exploration, confirm `command -v graphify` and `graphify-out/graph.json`, then use `graphify query`, `graphify path` or `graphify explain`. If unavailable, stop and ask for `pipx install graphifyy` and `graphify update .`; do not silently switch to grep-only research. After modifying code run `graphify update .`. Include this rule in code-exploration subagent prompts.

## Maintenance

Update this file whenever the project changes. Update each changed component README when its behavior, API, stack or operational contract changes. Update root runbooks, Compose/env docs and relevant ADRs together. Do not retain obsolete current-looking docs for removed APIs. Preserve unrelated user files and external Docker resources. Historical implementation audit is in `docs/architecture/legacy-audit.md`.
