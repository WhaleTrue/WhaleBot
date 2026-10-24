# WhaleBot dev implementation audit

## Scope and provenance

Audit source is the local `dev` tree, not main/master. The rewrite branch is `feat/whalemesh-architecture-rewrite`. The initial worktree already deleted `adapter-webui/server` and `orchestrator/server`; these user changes must not be restored. The configured origin is `git@github.com:WhaleMesh/WhaleBot.git`. Graphify was available and its existing knowledge graph was queried before source exploration.

Source-of-truth deployment inputs inspected: the entire root Compose file, remote-node Compose file, `.env.example`, service Dockerfile pattern, both root READMEs and AGENTS. Slice implementation evidence appears in [core](audit-core.md), [runtime](audit-runtime.md), [storage](audit-storage.md), [executor](audit-executor.md), [UI/adapters](audit-ui.md), and [schemas](audit-schemas.md). These documents identify inspected implementation paths rather than treating README descriptions as proof.

## Product behavior to retain

| Product | Existing external behavior | Rewrite requirement |
|---|---|---|
| Conversation | Web/Telegram messages, sessions, progress, replies and attachments | Authenticated adapters discover agent capability; preserve user identity separately |
| Agent | Bounded ReAct, tools, user-turn persistence before execution, skills, planning, secret references, traces and token metrics | Capability-only dependencies and explicit trusted approval enforcement |
| Models | Persisted multiple OpenAI-compatible profiles, selected active model, testing, tool calls and rate-limit handling | Multiple independent providers and models, truthful readiness, streaming, explicit cost/model constraints |
| Sessions | SQLite history, bounded context, idle expiry, append rejection after expiry, deletion | Durable concurrent updates and owner/provider binding |
| Memory | Notes and AES-GCM secret storage | Search, owner isolation, non-disclosing topology and secret resolution |
| Skills | Directory packages, metadata, reference files, search and ZIP import | Preserve knowledge packages, separate from callable capabilities |
| Workspace | Directory management; richer files and artifacts through userdocker | Confined isolated files/artifacts with bounds |
| Docker | Lifecycle, profiles/local images, estimate/pull, async exec, files, logs, copy, export, TTL, session/global scopes | Executor-only Engine access, uniform node identities and desired deployments |
| Remote execution | Outbound yamux connection to central hub | Optional authenticated relay transport without central business registry |
| Dashboard | Login, sessions, models, adapters, skills, secrets, Docker, events, stats and benchmark | Capability-resolving management plus actual dynamic topology |
| Diagnostics | Persistent events and optional rolling statistics | Optional ordinary capabilities, never required for chat |
| Benchmark | Persisted simulated planning/tool/ReAct cases plus optional Docker E2E | Actual new SDK calls and independent verification; no old proxy chain |

## Existing control/data flows

Web and Telegram send to the orchestrator, which forwards chat to runtime. Runtime reads session state, discovers components from the central registry, invokes llm-openai and dispatches a fixed Docker tool family. Most other HTTP APIs are orchestrator-specific reverse proxies. Each service repeats an unsigned registration client and sends a heartbeat every ten seconds to one configured orchestrator. Registry readiness combines heartbeat freshness and operational state. Selection uses service type/capability and is not a deterministic, policy-explainable resolver.

The default bridge is `whalebot_net`. Host ports expose orchestrator, dashboard and separate browser chat. Compose has fixed service/container names, fixed per-service URLs and extensive orchestrator `depends_on` relations. Runtime explicitly receives `ORCHESTRATOR_URL`, `SESSION_URL` and `LLM_OPENAI_URL`. Logger/stats/skills/memory callers additionally discover central registry entries. Remote manager identity and container identity use node-name/container-name rather than independently verifiable mesh instances.

Only user-docker-manager mounts the Engine socket. It creates Docker resources, injects the orchestrator URL, and rejects ordinary images unless they implement `userdocker.v1`. Build-only images are represented by permanent sleeping helper containers. Session container idle activity is manager-owned; global containers bypass idle cleanup. The auxiliary `env-golang` tree is not in current Compose. Its runner writes Go source into a temporary directory, runs `go run` with context timeout, buffers stdout/stderr, and removes temporary files. Code execution belongs behind authorized Executor operations after cutover, not a special environment role.

## Errors and security boundaries

Central heartbeat loss makes registry entries unavailable even if providers still exist. Registry names and shared tunnel token are not per-node trusted identity. Direct orchestrator routes bypass dashboard login. User, node and Docker authorization are not consistently separate. Legacy image approval trusts a caller boolean; node registration is unsigned. Several storage paths do not bind data to namespace plus principal, and some persistence errors are discarded. The UI audit also finds unauthenticated configuration boundaries and documented default credentials. Secret values require redaction from model context, tool output and diagnostics, not just encryption at rest.

New protocol must remove unsigned registration, role enums, shared-name replacement, special userdocker identity and request-asserted approval. Capability publication must be authorized independently from capability invocation. Stateful provider failover cannot silently migrate data. Dynamic creation must use separately provisioned child identities; it cannot create trust by advertising itself.

## Existing test coverage inspected

Legacy tests cover registry TTL/readiness/tunnel presence, remote tunnel routing, OpenAI URL normalization/rate-limit retry/profile key merging, planning parsing and Docker tool normalization, benchmark scoring and recovery scenarios, Telegram session identifiers/HTML formatting, image parsing/Docker stream decoding, workspace lexical paths and skill package/ZIP/search behavior. No test files were found in the inspected session, memory, workspace, logger, stats or browser auth trees. Test names and bodies inspected are not evidence of passing tests; old tests were not rerun for this audit.

Notable gaps: verified node identity, authorization and namespace separation, adversarial provider declarations, signed replay, deterministic resolution, partition recovery, independent seed loss, streamed cancellation, concurrent persistence, trusted Docker approvals and a real topology-driven UI. Rewrite tests must target these boundaries, not retain tests tied to incidental legacy wording or routing.

## Cutover inventory

Remove old orchestrator, registerclient copies, special UserDocker protocol, old role-based proxy APIs, central tunnel API, per-service Go modules and sleeping image helpers. Replace root Compose, env contract and root runbooks together. Existing named volumes are not silently migrated; no backwards-compatible protocol or database adapter is required. Do not delete external/user volumes during source cutover. Legacy assets and operational guides that describe deleted APIs must not remain presented as current documentation.

## Deployment decisions

A single SDK process can host multiple tightly related capabilities. Default deployment separates agent execution, model access, durable state, optional knowledge and browser privilege domains. Executor and relay remain optional. One-shot key provisioning is a deployment prerequisite only: it has no membership, discovery, resolution or business duties after startup. Runtime business dependencies are declared/resolved by capabilities, never by deployment package labels.
