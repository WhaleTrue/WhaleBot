# Browser node

`Register(*mesh.Node, dataDir string) ([]mesh.Capability, error)` installs the optional browser interface. Append `Requirements()` to the node manifest before starting the node. The package does not start a listener, register peer roles or coordinate other nodes.

The `web.browser` descriptor describes opening public `/`: no JSON input, HTML string output and read-only, idempotent semantics. Its empty scope union reflects that public route, not access to authenticated browser APIs. It does not advertise a universal RPC schema for the separate browser API routes below. Readiness indicates configured browser accounts.

## Credentials and identity

The initial account uses `WEB_ADMIN_USER` (default `admin`) and either `WEB_ADMIN_PASSWORD` or `WEB_ADMIN_PASSWORD_FILE` (default `/identity/admin-password`). There is no default password. Bootstrap credentials are read only when no persisted users exist. Passwords must contain 12–72 bytes and are stored as bcrypt hashes in `dataDir/web/users.json` with private file permissions. Missing bootstrap credentials leave login explicitly unconfigured.

Browser users are not Mesh principals. Random HttpOnly SameSite=Strict cookies reference server-side sessions with a 12-hour expiry. Restarting the web process invalidates sessions, not accounts. Mutations require `X-CSRF-Token` plus same-origin checks. Login throttles repeated failures by remote address. Cookies use Secure when TLS terminates in this process or `WEB_SECURE_COOKIE=true`; set that option behind an HTTPS reverse proxy. Forwarded headers are not trusted for cookie security.

The backend authorizes each advertised operation against the browser user's scopes, independently of the node's Mesh grants. Missing operation scopes fail closed. Account administrators can create accounts only with scopes they own. Password changes revoke every existing session for that account.

## Browser API

- `POST /api/auth/login` accepts `{username,password}` and returns `{user,csrf,owner,mesh_principal}`.
- `GET /api/auth/me` returns the same identity envelope. `POST /api/auth/logout` revokes the cookie.
- `PUT /api/auth/password` accepts `{current,password}`.
- `GET /api/users` and `PUT /api/users` require `web.users`; creation accepts `{username,password,scopes}`.
- `GET /api/topology` requires `mesh.inspect`. It returns `manifest`, `peers`, `bindings` (flat last-recorded local resolutions), `recorded_bindings` (the same observations grouped by `requirement_key`), `resolutions` (local requirement IDs mapped to `mesh.Explanation`), and `policies` (supported names). Explanations preview the next selection without advancing schedules, creating sticky pins, changing bindings or writing disk. Recorded resolutions are not active traffic; peer bindings are unknown.
- `PUT /api/policy` requires `mesh.policy` and accepts `{requirement,policy,pin}`. Only this node's declared requirement IDs are mutable.
- Each explanation contains effective `requirement`, `requirement_key`, `policy`, `sticky`, optional saved `sticky_pin`, `rationale`, `selected`, `providers` and optional `error`. Every provider has identity, capability, eligibility/selection flags, one-based policy rank for eligible candidates, `locality_rank`, priority/load/weight inputs, and structured `reasons`. Required-dependency reasons nest provider evidence; `required_cycle` explicitly marks revisited required paths. Missing optional requirements remain visible with an empty selection.
- `POST /api/call` accepts `{capability,operation,node?,path?,input?,stream?}`. The backend resolves a ready provider through the SDK, validates the exact advertised operation and path, and forwards JSON, SSE or binary responses. `node` is the canonical escaped namespace/principal/name/instance identity key, not an arbitrary URL or raw instance ID. Query parameters are supported on advertised paths. Template parameters cannot change route structure.
- `POST /api/container/approval` requires the browser user's `executor.admin` scope and accepts `{image,node?}`. It signs a one-hour `image.use` approval for an immutable digest, this user's owner and this namespace. The web identity key is read from `MESH_IDENTITY_FILE`; the executor must trust the corresponding approval public key. There is no approval boolean.

Client-supplied `owner` and `user_id` are discarded. The backend injects a stable owner derived from the authenticated account for session, memory, skills, workspace, secret, event, statistic, benchmark, agent and executor requests. Agent session IDs are also namespaced. Providers require the corresponding delegated Mesh grants. Raw `/secrets/resolve` is forbidden through the browser API, including for administrators; runtime tools resolve placeholders instead. Executor delegation must disable node-administrator ownership bypasses.

## Static assets

`WEB_STATIC_DIR` defaults to `/app/web`. Copy the production output of `web/` there. Static files and login are public; all other browser APIs use cookie identity. Security headers include CSP, frame denial, MIME sniff prevention and same-origin referrer policy. Missing assets produce an explicit HTTP 503 rather than a fake dashboard.

Runnable checks: `go test ./internal/web`. Tests cover CSRF, session expiry, scope separation, owner injection, cross-user session rejection, raw-secret denial and operation path constraints.
