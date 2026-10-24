# Runtime and LLM implementation audit

## Evidence inspected before implementation

Graphify queries traced the legacy runtime ReAct loop, session flow, model invocation, persisted model profiles, benchmark cases, and OpenAI client. Sources inspected: `runtime/cmd/server/main.go` (run handler, ReAct loop, dispatch, secret resolution and redaction), `runtime/cmd/server/benchmark.go` (persistent results, simulated tool/ReAct cases and end-to-end checkpoints), `llm-openai/internal/openai/openai.go`, `llm-openai/internal/configstore/store.go`, and `llm-openai/cmd/server/main.go`.

## Existing real behavior

The legacy runtime loads session history, persists the user turn before model execution, constructs a system prompt with optional skill injection, and runs a bounded multi-step tool loop. It accumulates model usage, sends optional logger/statistics events, resolves secret placeholders only at execution, redacts known secret values, and gathers exported artifact attachments. Session writes and catalog errors often only log failures. Tool dispatch is a fixed Docker-specific switch. Routing depends on an orchestrator and configured service URLs. Risk gating depends on a model classifier and natural-language confirmation, not a trusted approval record.

The legacy LLM provider persists multiple named profiles with masked public keys and an active model. Its real OpenAI-compatible HTTP client supports tool calls, cancellation and HTTP 429 retries, but not streamed tool deltas. Missing API keys silently enable echo fallback. Localhost upstream URLs are silently rewritten. Profile replacement updates memory before all validation and disk persistence complete.

The benchmark is substantive: persisted history, usage and latency, structured planning classification, tool argument checks, canned multi-step ReAct scenarios, and optional Docker end-to-end checkpoints. It currently routes through orchestrator URLs and a self HTTP URL.

## Rewrite decisions

`internal/agent` discovers required session/LLM capabilities and optional skill, memory, telemetry and exported tool capabilities through the mesh SDK. Required failures are errors. A turn is persisted before any model call. Tool invocation uses only signed, trusted capability exports and separately authorized, argument-bound approvals. Secret placeholders remain opaque to the model; resolved values are redacted from results, output, traces and persisted history. Streaming follows request cancellation. Benchmark simulated mode calls a real selected LLM against clearly labeled canned observations; end-to-end mode invokes the real agent and verifies independently through discovered capabilities.

`internal/llm` uses only the standard library. Configuration is validated then persisted before publication, supports explicit model selection without inference-based or cost-based switching, and never substitutes mock output on upstream failure. Mock mode requires an explicit environment setting and labels its responses. OpenAI requests support streamed content, indexed tool-call fragments, usage, bounded bodies and deadlines. Only explicit rate-limit rejection is retried; ambiguous transport failures and partially consumed streams are never replayed.

## Operational boundaries

Real model inference requires a configured upstream. Docker end-to-end benchmark requires executor capabilities, a permitted image, and trusted approvals where policy requires them. Optional discovery failures do not create fabricated capability success. Runtime behavior checks use `go test ./internal/agent`; real Docker integration uses `python scripts/acceptance.py`.
