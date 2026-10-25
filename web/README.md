# WhaleMesh browser application

This optional Svelte 5/Vite 8 application uses the same-origin cookie-authenticated API of `internal/web`. It has no orchestrator address, provider URL setting or central registration dependency.

## Development and production

```sh
npm ci
npm run dev
npm run build
```

The development server proxies `/api` to `http://localhost:18000`. Production output is `dist/`; copy its contents to the web node's `WEB_STATIC_DIR` (default `/app/web`). The production application has no CDN dependency. The lockfile pins installed packages.

Retrieve the initial administrator password from the web identity volume. The default deployment uses `/identity/admin-password`. Account passwords, user scopes and later changes persist in the backend. There is no default plaintext password. Deploy behind HTTPS and enable `WEB_SECURE_COOKIE=true` when TLS terminates upstream.

## Interfaces

Chat streams delta, trace, done and error events, renders sanitized Markdown, shows usage and tool progress, manages private sessions, cancels active browser requests and downloads real artifacts. Exact tool arguments can be reviewed before a scoped one-use approval call. Approval is not automatic.

Models edits multiple profiles, cost classes, tool/streaming features and write-only keys. Provider selectors show the actual selected identity; stale configuration reads cannot overwrite a different provider's editor. Removing an active model clears its selection; saving no active model or an empty configuration is supported. Telegram has token replacement/removal, a whitelist editor, idle-session settings and real status/error inspection. Missing providers remain unavailable, never mock records.

Sessions, skills, secrets, workspace, artifacts, Docker, logs and statistics use the shared capability operation interface. It discovers actual advertised operations, shows their schemas and exact authorization scopes, accepts structured JSON, provides file upload/base64 editing, renders returned records, supports selecting a record into a request and downloads binary or JSON responses. Read and write operations on the same path preserve the request editor so configuration can be edited without retyping. Provider responses are not reduced to canned role-specific dashboard data. Raw secret resolution is intentionally absent and denied by the backend.

Docker includes image/deployment/container/exec/file/job operations and a trusted signed image-approval form. Only immutable digest references can be approved. The request interface never converts a browser boolean into permission.

Benchmark has a dedicated provider/model selector, asynchronous start/polling, persisted history, per-case evidence, single-run deletion, clear-history confirmation, per-run/all JSON and CSV downloads. Daily score (`scores.total` plus Plan gate/Tool call/ReAct), Bonus and E2E remain separate stable columns. Zero scores remain zero; missing or unfinished scores are dashes. The newest run's case set is prominent; older versions remain readable but secondary. Best compares only completed results in that latest case set, never historical versions. Model identity, upstream name, provider and frozen revision remain inspectable. CSV escapes quotes/newlines and prevents spreadsheet formula execution in text fields.

Real E2E prepares fresh nonce-bound build and verification commands for an existing user-owned container. The full arguments are shown before the user grants two exact one-use `agent.approve` calls. It uses separate build/run directories in one container, not a two-container deployment. Preparation, approval and run stay on the selected agent identity; unavailable providers do not silently move stored history. The advanced JSON console remains available for all advertised benchmark operations. See [Benchmark automation](../docs/benchmark-automation.md) for authenticated batch evaluation and authorization prerequisites.

Topology renders dynamic nodes, capabilities and requirement declarations from the current snapshot. Required declarations use purple links and solid requirement outlines; optional declarations use dotted amber links and outlines. Declaration candidate links match capability ID/interface only: they do not establish trust, version compatibility, readiness, scopes or resolver eligibility. Remote requirements remain declared candidates with unknown availability; the browser never invents remote bindings or remote cycle diagnoses.

Local requirement markers use the endpoint's read-only explanations as authoritative evidence. Unavailable selections, optional dependencies with no provider and nested cycle evidence remain visible. Details summarize effective policy, sticky boundary, next-selection results, eligible/rejected providers and rejection codes/messages, with full nested JSON available. Solid green edges show recorded local bindings grouped by `requirement_key`; dashed cyan edges show next-selection previews, not recorded selections. Historical targets absent from current membership are labeled binding snapshots and do not become current declaration candidates. Legacy flat records without requirement attribution attach to the local node, never to a guessed requirement.

Pink relay edges require an advertised transport URL of the actual form `<relay-base>/relay/forward/<principal>/<instance>` matching the advertising node's identity. A uniquely matching advertised hub base identifies the other endpoint; otherwise the graph shows an endpoint with unknown hub identity. These are observed advertisements, not proof of a live tunnel or traffic. A `mesh.relay` capability alone creates no route and changes no node color. Node and capability details expose full identity, host, transport, lifecycle, operations and scopes. Graph objects and edges support mouse and keyboard inspection.

Opening, inspecting and refreshing topology only read the snapshot; they do not resolve/select providers, advance policy schedules, create sticky pins or overwrite recorded bindings. Policy options come from the endpoint's `policies` list. Explicit policy saves apply only to this node's declared requirements, not peer policy or trust, and do not select providers. Saving a policy/pin clears that requirement's saved sticky boundary.

The graph helper has standalone behavior coverage for recorded-versus-preview edges, required/optional declarations, unavailable markers, nested local cycle evidence, remote unknowns and advertised relay routes. Run frontend checks from the repository root with `node --test web/src/api.test.js web/src/topology.test.js`.

Account controls change passwords and create users with explicit scope subsets. The browser never receives Mesh signing keys. Per-operation user authorization remains mandatory even when the web node itself has broad Mesh grants.
