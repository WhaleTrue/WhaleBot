# Model capabilities

`llm.Register` embeds an ordinary provider in any mesh node. No service name, orchestrator URL or role lookup is used. It advertises `llm.invoke` and `llm.models.list`; one provider can contain multiple saved model profiles and multiple providers can coexist.

## Operations

- `POST /llm/invoke`: typed messages/tools, required exact saved `model` ID and `model_revision`, optional `free_only`, streaming and completion budget; scope `llm.invoke`.
- `GET /llm/models`: public model catalog; scope `llm.models.list`.
- `GET /llm/config`: masked persisted configuration; scope `llm.config.read`.
- `PUT /llm/config`: validated atomic configuration replacement; scope `llm.config.write`.

The catalog capability advertises all operation scopes as a union. Browser and node execution check the chosen operation, not a requirement for every caller to possess all configuration rights.

Primary schemas describe the real HTTP values. Catalog/config GET requests have no JSON input; PUT accepts the typed configuration document. The input schema is their union, while output is the public configuration/catalog document. Capability semantics summarize mutation; GET operations remain read-only and idempotent. PUT is not idempotent because every replacement issues a new revision.

The `PUT /llm/config` operation declares `transport_security=tls` because configuration writes carry API keys. Production callers and servers require TLS for this operation. Public catalog reads and masked configuration reads do not require TLS solely because they share its capability. Only isolated development deployments may explicitly set `MESH_ALLOW_PLAINTEXT_SENSITIVE=1`; this permits plaintext sensitive traffic and must not be enabled on untrusted networks.

## Configuration

Persisted at `<MESH_DATA>/llm-config.json`:

```json
{
  "active_model_id": "local-tools",
  "models": [{
    "id": "local-tools",
    "name": "Local tool model",
    "base_url": "http://host.docker.internal:8080/v1",
    "model": "my-model",
    "api_key": "",
    "timeout_seconds": 120,
    "cost_class": "free",
    "capabilities": ["tools", "streaming"]
  }]
}
```

`id` is the saved profile ID used in invocation. `model` is the upstream model identifier. Provider identity is the mesh principal/instance and is not either of those IDs. `base_url` may end at `/v1` or `/chat/completions`; it is never silently rewritten from localhost to another host. Docker must actually be able to reach the configured address.

Cost class is `free`, `paid` or `unknown`; omission is treated conservatively as unknown for automatic routing. Capabilities explicitly list `tools` and/or `streaming`. Unsupported operations fail before calling the upstream. Empty capabilities permit plain text requests. A missing default active model does not silently select the first saved model; an explicit saved profile can still be invoked.

Public config/catalog responses include `has_api_key`, never the key. An empty key during replacement preserves the existing write-only key for that profile. Delete/recreate a profile if intentionally discarding it. Configuration is validated and persisted before becoming active.

Every configuration has an opaque server-generated `revision`, persisted across restarts and exposed without secrets in config/catalog responses. Every successful replacement generates a new revision, including an unchanged replacement; a caller-supplied revision is ignored. Existing configuration files without a revision receive one on load. The invocation capability exposes the catalog revision as `model_revision`.

Callers must resolve the exact profile ID and catalog revision before inference. Invocation rejects missing/stale revisions or unavailable profiles before contacting the upstream, including streaming requests. `free_only=true` additionally requires the selected profile's current cost class to be explicitly `free`. A profile snapshot fixes endpoint, upstream model, credentials, cost and features for that invocation. Changes invalidate subsequent requests rather than silently changing an in-flight run. Explicit paid/unknown profiles remain callable with their current revision and without `free_only`.

## Explicit development provider

`LLM_MOCK=1` advertises one free catalog model, `mock`, and labels every response `[MOCK: LLM_MOCK=1]`. It is deterministic echo output, not real inference or real tool calling. Asking this provider for another model or supplying tools is an error. Its revision is prefixed `mock:` so real and mock selections cannot interchange. External failures never enable the mock. Disable the environment flag and configure a real profile for actual model evaluation.

The active invocation capability publishes readiness, default cost class and tool/streaming attributes. Runtime applies explicit model/provider constraints and never broadens a failed request to a potentially paid account.

## Transport and retries

Actual OpenAI-compatible HTTP supports typed tool calls, indexed streamed tool fragments, SSE, usage and cancellation. Default upstream budget is 120 seconds; profile timeout is bounded at 1800 seconds and inherits the tighter request context. Response and line sizes are bounded. Inference is never automatically replayed, including HTTP 429, transport failures and partly consumed streams. This is a fail-fast policy: upstream HTTP 429 is visible in the caller's `upstream_error` message; non-streaming invocation uses HTTP 502, while streaming emits an `error` event. Retrying requires an explicit new request and a current model revision. SDK and browser streaming retain backpressure through bounded writes rather than buffering an unbounded completion.

Run package checks from repository root:

```sh
go test ./internal/llm ./internal/agent
```

Tests use local HTTP providers; real upstream credentials and model quality remain operator-specific verification.
