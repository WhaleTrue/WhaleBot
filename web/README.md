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

Nodes is the default home page. Its responsive tile grid lists current local/peer manifests, readiness, capabilities and requirement counts, with a name/namespace/capability filter. Opening a tile keeps the full namespace/principal/name/instance identity: model and Telegram settings use only that node's providers. Nodes without a dedicated settings editor expose manifest inspection and their advertised operation console, not invented configuration. Historical binding-only targets remain in Topology, not in the current-node grid.

The interface follows [xianii-theme](https://github.com/Nigh/xianii-theme): charcoal surfaces, pink primary controls, semantic status colors, consistent 8px control corners and shared spacing. Theme values are implemented locally without a CDN or additional frontend dependencies. Desktop layout is the priority; mobile provides basic wrapping and single-column usability, without a separate mobile interaction system.

Typography uses shared role tokens: page headings 30px, section headings 20px, subheadings 18px, body 16px, controls/labels/tables 14px and dense metadata/code 13px at the default browser size. Rem-based sizing preserves browser font preferences. Node tiles keep supporting text at normal weight, with semibold reserved for headings. Descriptions use a 70ch reading measure and relaxed leading; numeric data uses tabular figures.

Inter is intentionally retained from the Xianii reference, rather than changed to satisfy generic font-popularity heuristics. Its 48,256-byte Latin variable WOFF2 is self-hosted at `public/fonts/inter-latin-wght-normal.woff2`, sourced from `@fontsource-variable/inter@5.2.8`; its SIL OFL license is adjacent. Only upright Latin is downloaded, using `font-display: swap`; Chinese and other scripts use installed system fallbacks. No font CDN or font package is needed at runtime.

Chat streams delta, trace, done and error events, renders sanitized Markdown, shows usage and tool progress, manages private sessions, cancels active browser requests and downloads real artifacts. Exact tool arguments can be reviewed before a scoped one-use approval call. Approval is not automatic.

Models edits multiple profiles, cost classes, tool/streaming features and write-only keys. Provider selectors show the actual selected identity; stale configuration reads cannot overwrite a different provider's editor. Removing an active model clears its selection; saving no active model or an empty configuration is supported. Telegram has token replacement/removal, a whitelist editor, idle-session settings and real status/error inspection. Missing providers remain unavailable, never mock records.

Sessions, skills, secrets, workspace, artifacts, Docker, logs and statistics use the shared capability operation interface. It discovers actual advertised operations, shows their schemas and exact authorization scopes, accepts structured JSON, provides file upload/base64 editing, renders returned records, supports selecting a record into a request and downloads binary or JSON responses. Read and write operations on the same path preserve the request editor so configuration can be edited without retyping. Provider responses are not reduced to canned role-specific dashboard data. Raw secret resolution is intentionally absent and denied by the backend.

Docker includes image/deployment/container/exec/file/job operations and a trusted signed image-approval form. Only immutable digest references can be approved. The request interface never converts a browser boolean into permission.

Benchmark has a dedicated provider/model selector, asynchronous start/polling, persisted history, per-case evidence, single-run deletion, clear-history confirmation, per-run/all JSON and CSV downloads. Daily score (`scores.total` plus Plan gate/Tool call/ReAct), Bonus and E2E remain separate stable columns. Zero scores remain zero; missing or unfinished scores are dashes. The newest run's case set is prominent; older versions remain readable but secondary. Best compares only completed results in that latest case set, never historical versions. Model identity, upstream name, provider and frozen revision remain inspectable. CSV escapes quotes/newlines and prevents spreadsheet formula execution in text fields.

Real E2E prepares fresh nonce-bound build and verification commands for an existing user-owned container. The full arguments are shown before the user grants two exact one-use `agent.approve` calls. It uses separate build/run directories in one container, not a two-container deployment. Preparation, approval and run stay on the selected agent identity; unavailable providers do not silently move stored history. The advanced JSON console remains available for all advertised benchmark operations. See [Benchmark automation](../docs/benchmark-automation.md) for authenticated batch evaluation and authorization prerequisites.

Topology uses a vertically scrolling, node-focused relationship browser instead of a global crossing-edge graph. Choose a current or binding-snapshot node, inspect its provided capabilities and required/optional declarations, and expand candidate lists only when needed. The display filter changes presentation only. Declaration candidates match capability ID/interface only: they do not establish trust, version compatibility, readiness, scopes or resolver eligibility. Remote requirements retain unknown availability; the browser never invents remote bindings or remote cycle diagnoses.

Local explanations remain authoritative. Unavailable selections, optional dependencies without providers and nested cycle evidence stay visible. Details summarize effective policy, sticky boundary, next-selection results, eligible/rejected providers and rejection codes/messages, with full nested JSON. Recorded local bindings and next-selection previews have separate labeled sections. Historical targets absent from membership are explicitly binding snapshots, never current declaration candidates. Legacy flat records without requirement attribution attach to the local node, not a guessed requirement.

Advertised relay routes require a transport URL of the actual form `<relay-base>/relay/forward/<principal>/<instance>` matching the advertising identity. A uniquely matching advertised hub base identifies the other endpoint; otherwise its hub identity remains unknown. These are advertisements, not proof of a live tunnel or traffic. A `mesh.relay` capability alone creates no route. Node/capability inspection exposes full identity, host, transport, lifecycle, operations and scopes. Native buttons and disclosure controls support keyboard navigation; JSON inspection moves focus to the details section. Long identities and JSON wrap within the viewport; Topology requires no horizontal scrolling.

Opening, inspecting and refreshing topology only read the snapshot; they do not resolve/select providers, advance policy schedules, create sticky pins or overwrite recorded bindings. Policy options come from the endpoint's `policies` list. Explicit policy saves apply only to this node's declared requirements, not peer policy or trust, and do not select providers. Saving a policy/pin clears that requirement's saved sticky boundary.

The graph helper has standalone behavior coverage for recorded-versus-preview edges, required/optional declarations, unavailable markers, nested local cycle evidence, remote unknowns and advertised relay routes. Run frontend checks from the repository root with `node --test web/src/api.test.js web/src/topology.test.js`.

Account controls change passwords and create users with explicit scope subsets. The browser never receives Mesh signing keys. Per-operation user authorization remains mandatory even when the web node itself has broad Mesh grants.
