# Telegram capability adapter

`Register(*mesh.Node, dataDir string) ([]mesh.Capability, error)` advertises `adapter.telegram`, installs signed configuration/status operations and loads private persistent settings. Append `Requirements()` to the node manifest. Call `Start(context.Context)` after `Node.Start`; cancellation stops polling and active agent streams.

## Configuration

The `adapter.config` scope controls `GET /telegram/config`, `PUT /telegram/config` and `GET /telegram/status`. Reads never return the token. PUT requires TLS and accepts `{token?,allowed_user_ids?,idle_seconds?}`. Omitting token preserves it; an empty token removes it. Omitting `allowed_user_ids` clears the whitelist. Idle timeout defaults to 86400 seconds and must be at least 60 seconds.

Initial settings can come from `TELEGRAM_BOT_TOKEN` and comma-separated `TELEGRAM_ALLOWED_USER_IDS`. Configuration, polling cursor and conversation state persist in `dataDir/telegram/config.json` with private permissions. Changes cancel and restart the poller. Missing token or an empty whitelist means **unconfigured**, not ready or unrestricted. The configuration capability itself remains ready so an administrator can configure it. Status reports polling, errors and persisted conversation count.

The capability publishes non-null input/output JSON Schemas: input is no-input or the configuration object; output is public configuration or status. The operations declare their own scopes and semantics. The capability summarizes mutation because PUT changes configuration; GET operations remain read-only. Configuration readiness does not claim that Telegram polling is configured.

## Behavior

All Telegram traffic is outbound HTTPS to the official Bot API. The adapter verifies the token using `getMe`, registers commands and uses long polling with a 45-second server timeout. Transport failures remain observable and polling resumes after a delay. Request-construction failures never expose URLs. API error descriptions redact the token captured for that request, including URL-escaped forms, even after configuration rotates the token.

Only whitelisted sender IDs are processed. Owners include both sender ID and chat ID, preventing group participants from sharing private sessions. `/new` and `/reset` rotate sessions; `/end` and `/stop` end them; `/status` inspects state; `/help` lists commands. Idle conversations rotate before the next message. Session IDs and conversation state survive restarts.

Agent calls resolve `agent.run` through the SDK and carry the verified owner in signed JSON. Trace SSE events update a progress message. Done events produce escaped HTML messages and authenticated artifacts. Error events and prematurely closed streams produce explicit failure messages. Text is split into bounded chunks before HTML escaping, so Telegram length limits do not corrupt tags. The adapter does not enable arbitrary HTML from model output.

Artifact URLs must be relative advertised GET operations of `artifact.export`. Downloads use the SDK, select the executor principal carried in the URL, and inject the verified owner into the signed query. Files above 45 MiB are rejected before Telegram upload. The node needs `agent.run`, `agent.delegate`, `artifact.export` and `container.delegate` grants. It never grants tool approvals on behalf of users.

The poller processes updates serially. This deliberately bounds concurrent agent runs; independent per-chat queues are the upgrade if simultaneous conversations become necessary. Cursor persistence gives at-least-once behavior across a crash between delivery and cursor storage. Incoming document ingestion is not advertised; non-text messages receive an explicit explanation rather than being silently treated as text.

Runnable check: `go test ./internal/telegram`. Checks cover private configuration persistence, token masking, empty-whitelist status and token-safe diagnostics after malformed URLs or token rotation.
