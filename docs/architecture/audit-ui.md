# UI and Telegram implementation audit

## Evidence inspected

Graph query: `graphify query "webui adapter-webui adapter-telegram authentication chat streaming sessions configuration topology"`. The query identifies the dashboard, chat frontend stores, Telegram conversation manager, progress tracker, outbound polling, command handler and artifact delivery. Source review covers `webui/authsrv/main.go`, `webui/src/lib/api.js`, `webui/package.json`, `adapter-webui/README.md`, `adapter-webui/web/src` and `adapter-telegram/cmd/server/main.go`.

## Existing behavior and defects

The dashboard uses Svelte 4 and Vite. Its API client assumes one orchestrator URL and encodes provider roles into individual proxy paths. Dashboard authentication uses bcrypt but creates a documented default administrator password. The separate chat adapter documents JWT cookies, session management, progress polling and attachments. Its configuration endpoint permits unauthenticated credential updates. The adapter's deleted server directory is a pre-existing user change and is not reconstructed.

Telegram uses outbound long polling, persistent adapter settings, a user allowlist, conversation rotation, commands, progress messages and artifact upload. Empty allowlists permit every user. Conversation state is in memory. Requests and expiry checks depend directly on orchestrator routes. HTML delivery has plaintext fallback and transport retry logic. Artifact payloads are base64 with an upload size ceiling. The graph includes progress traces and tool descriptions which must remain observable without an orchestrator.

## Replacement

The optional web package serves one Svelte application and a cookie-authenticated browser API. Browser accounts are separate from signed Mesh node principals. Passwords use bcrypt. Random server-side sessions use HttpOnly SameSite cookies, expiry, CSRF tokens, same-origin checks and explicit scope grants. No default credentials are created. Browser requests resolve advertised capabilities and operations through the Mesh SDK; provider URLs and role-specific proxies are not configuration inputs. User-owned chat sessions are isolated from other accounts.

Topology is derived from current manifests, requirement resolutions and active bindings, not an illustrative static diagram. Capability, dependency, binding and relay edges have separate display semantics. Policy edits apply only to this web node's declared requirements; trust grants are not editable from the browser.

Telegram retains outbound-only Bot API polling. Token, allowlist, cursor and conversation state persist privately. Missing token or allowlist is reported as unconfigured, not healthy. Commands, session rotation, escaped HTML messages, progress/error events and real artifacts use generic SDK resolution. Secrets are never returned by configuration reads.

## Verification boundary

Backend security and session checks use `go test ./internal/web ./internal/telegram`. Frontend production assets require `npm ci && npm run build` in `web/` and must be copied to the configured static directory. Browser acceptance exercises the real deployed surface.
