# Agent runtime

This package installs ordinary `agent.run`, `agent.approve`, `benchmark.run` and `benchmark.results` capabilities. It neither owns Docker Engine nor selects providers through a central service. `Register(node, dataDir)` returns descriptors; the caller aggregates them and adds `Requirements()` before starting the node. Lifecycle workers use `Node.AddWorker`.

## Dependencies and state

Required capabilities are `session.store` (`agent.sessions`, sticky) and `llm.invoke` (`agent.model`). Optional capabilities are memory search, skill search, secret resolution, event writing and statistics ingestion. Losing optional retrieval or telemetry does not fabricate results. Losing session persistence or model invocation fails the run.

Sessions live in the selected provider, not in agent files. Agent local state includes `state-id.json`, `approvals.json` and `benchmarks.json`, written through private temporary files and atomic rename. Preserve this data volume and node identity. Advertised `state_id` identifies this agent's persistent boundary. Another agent/store is not automatic migration.

An owner defaults to `node:<authenticated-principal>`. Supplying any explicit `owner` requires node scope `agent.delegate`. Browser/IM adapters must derive ownership from authenticated users; do not pass model-supplied owners as authority.

## APIs

All routes require signed mesh requests. JSON decoding rejects unknown fields and multiple values; request bodies are capped at 1 MiB.

- `POST /agent/run`, scope `agent.run`: `{session_id?,message,model?,provider?,model_revision?,stream?,owner?}`. An absent session ID is generated. IDs accept letters, digits and `_.:@/-`, up to 192 characters. Message must be nonempty, at most 128 KiB. An optional `model_revision` constrains selection to that exact catalog revision.
- `POST /agent/approvals`, scope `agent.approve`: `{session_id,tool,arguments,owner?}`. Returns approval `id` and `expires_at`.
- `POST /benchmark/run`, scope `benchmark.run`: `{model?,provider?,include_e2e,session_id?,container_id?,owner?}`. Returns HTTP 202 with persisted run record.
- `GET /benchmark/runs`, scope `benchmark.read`: optional trusted delegated `owner` query. Returns `{runs:[...]}` for that owner.
- `DELETE /benchmark/runs` or `/benchmark/runs/{id}`, scope `benchmark.delete`: deletes all completed records for that owner or one record. Running records cannot be deleted.

A normal run returns `{session_id,model,provider,model_revision,upstream_model,reply,usage,attachments}`. Model identity fields describe the selected profile, provider principal and upstream model. Attachments contain `name`, `url` and optional `media_type`; URLs still need authenticated adapter handling. A streaming run emits SSE `trace`, `delta`, `done`, or `error`. A stream error after headers is an SSE error, not a second HTTP status. `done` contains the same final response as non-streaming mode.

Only one run per owner/session executes concurrently on one runtime; another gets HTTP 409 `session_busy`. This is not a distributed session lock. Provider revision checks detect conflicting writes across runtimes. The handler has a ten-minute budget, but signed SDK requests currently cap individual calls at five minutes; callers can impose shorter deadlines. Cancellation propagates to dependencies.

## Model selection and cost

Selection happens once per run:

- No `model` and no `provider`: only a provider explicitly advertising `cost_class=free` is eligible. Its current catalog profile must also be explicitly free.
- Explicit `provider`: pins a provider by principal, logical name or canonical escaped `mesh.IdentityKey`. Bare instance IDs are not selectors. Its configured active model resolves to an exact saved ID when `model` is omitted.
- Explicit `model`: queries authorized providers' `llm.models.list` catalogs and requires an exact model ID. Missing models fail; no silent replacement.
- The same model on multiple providers, with any paid or unknown-cost option, requires explicit `provider`. Supply both `model` and `provider` for an unambiguous paid selection.

The selected model's declared tool/stream support controls request features. Models without tools receive none. No upstream error becomes mock output. Development mock behavior must be explicitly enabled by the [LLM provider](../llm/README.md).

Runtime and benchmark share one selection path. Selection captures the exact profile ID, provider binding, upstream model, catalog revision and whether free-only authorization applies. Every ReAct step sends the same ID/revision and free-only constraint. The provider checks revision, cost and declared features before any upstream request, including streaming. Any configuration replacement invalidates later inference steps; no reselection, fallback or inference replay occurs. A completed upstream call may finish using its original immutable profile snapshot.

Session metadata and final output record `model`, `provider`, `model_revision` and `upstream_model`. Selection failures still leave the new user turn persisted and attempt `state=failed`.

## Execution and recovery

The runtime reads up to 80 context messages from its sticky session provider. It persists the new user message and `state=running` before retrieval, inference or tool execution. Each assistant/tool observation is appended with `expected_revision`. Successful runs mark `state=completed`; failures attempt a bounded final `state=failed` update.

The ReAct loop permits at most 16 model steps. The last step removes tools. Model requests allow up to 4096 output tokens. Tool routes are rediscovered each step. Duplicate exported tool names are removed rather than selected arbitrarily. Tool definitions come only from resolved, trusted capability descriptors, not from model-generated URLs.

Interrupted persisted tool calls are represented as outcome unknown on the next turn. They are not replayed. Inspect current state before retrying a mutation; neither reconnect nor persistence provides exactly-once execution.

Optional retrieval shares a three-second budget: up to three skill results and five memory notes. Skill snippets are limited to 16000 bytes each; memory snippets to 8000 bytes each. Retrieved content is untrusted context, never an authorization decision. Event/stat calls each have a one-second budget; their failure does not discard a successful run. Traces record correlation IDs, session, step, event, tool, duration and usage. They are not proof a tool succeeded unless its observation confirms success.

## Tools, approvals and secrets

Only exported GET/POST/PUT/DELETE tools with usable JSON Schema and local application paths are exposed. Arguments must be a JSON object, at most 64 KiB, and pass the export's schema after trusted owner injection. Results are limited to 256 KiB. Workspace/file access and Docker policy remain destination responsibilities.

A tool needs approval if its export requests confirmation or its capability is mutating, side-effecting, or not explicitly read-only. The separate `agent.approve` route validates the tool and exact arguments. Approval binds owner, session, provider principal, capability, method, path, input schema and canonical arguments. It expires after ten minutes and is consumed durably before execution. Up to 10000 unexpired approvals are retained. User text and `approved=true` arguments cannot grant approval. Changed arguments, provider or schema need new approval. Failure after consumption does not restore approval.

Agent approval is distinct from Executor `image.use` approval. A tool can pass the agent gate and still fail destination policy.

Keep secrets as `{{secret:key}}` placeholders in prompts and arguments. After approval, the runtime resolves placeholders under verified owner through `secrets.resolve`, then injects the owner again. Raw values are transient; known values and JSON-escaped forms are redacted from persisted tool results, replies and streamed deltas. Do not paste raw secrets into prompts: this redaction only knows values resolved during the run. Secret transport still requires HTTPS unless the SDK's isolated-development exception is explicitly enabled. Browser raw-secret resolution is denied.

## Benchmarks and limits

One benchmark runs at a time per runtime, with a 15-minute worker budget. The newest 100 records persist. A restart changes persisted running records to failed; shutdown also marks running records failed. Model selection uses the same cost rules as agent runs. All cases share one frozen profile/revision; the optional real agent run receives that exact model, provider and revision. Any inference failure stops further cases without replay. Persisted benchmark metadata records the selected profile, upstream model, provider and revision.

The `mesh-v1` case set includes plan/approval decisions and simulated tool/ReAct scenarios with canned observations. Those simulations are not evidence of real Docker execution. The optional end-to-end portion requires both a session ID and an approved owned container ID; it uses real discovered capabilities and can fail independently. Category scores are percentages; total weights are 25% plan gate, 35% tool call and 40% ReAct. Inspect individual cases and errors, not only status or aggregate score.

Persisted state reads reject files above 8 MiB. Provider history, tool limits, signed request deadlines and actual upstream availability also bound runs. No real model, Telegram, browser or Docker result is claimed by this document.

Related: [discovery/resolver](../../docs/discovery-resolver.md), [provider APIs](../providers/README.md), [Executor policy](../executor/README.md), [troubleshooting](../../docs/troubleshooting.md). Runnable checks for integration owner: `go test ./internal/agent` and root acceptance workflow.
