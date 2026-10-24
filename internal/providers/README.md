# Durable providers

`Register(node, dataDir)` installs selected storage capabilities and returns their descriptors. `PROVIDER_CAPABILITIES` is a comma-separated allowlist; empty selects all eight supported capabilities. Unknown IDs fail registration. Call `Close(node)` after node shutdown to close SQLite. This package starts no central service and migrates no data between providers.

## Persistence and ownership

`dataDir/providers.db` uses SQLite WAL, `synchronous=FULL`, a ten-second busy timeout and one connection. Records are keyed by namespace, verified owner, kind and ID. A persisted database `state_id` appears in every capability, together with `stateful=true`, `sticky=required`, `persistence=sqlite` and `storage_boundary=<provider-principal>`.

An omitted `owner` selects `principal:<authenticated-principal>`. An explicit owner requires the corresponding `<family>.delegate` node scope. Explicit `node:<principal>` maps to that principal's storage; other explicit owners map to user storage. Delegation families are `session`, `memory`, `secrets`, `skills`, `workspace`, `events` and `stats`. Browser users are not mesh principals: adapters must derive owner from authenticated user identity and also hold operation scopes. Client/model-supplied owner strings do not create authority.

Preserve both node identity and data. Another provider with the same capability is a different storage boundary. Sticky resolution refuses automatic migration; changing policy does not copy data. See [discovery and resolver](../../docs/discovery-resolver.md).

All API requests require signed mesh authentication. JSON bodies must contain exactly one object, reject unknown fields, and are capped at 12 MiB. SDK's overall transport cap is 16 MiB. Byte fields use standard JSON base64 strings. Common pagination is `{limit?,offset?}`, default 50, maximum 200, offset 0–100000. Common IDs are nonempty, at most 256 bytes, without NUL/CR/LF. Metadata allows 64 entries, values up to 4096 bytes. Errors use HTTP 400/403/404/409/413 where applicable; unexpected storage errors return a generic HTTP 500.

## Sessions: `session.store`

- `POST /session/context`, `session.read`: `{session_id,limit?,owner?}`. Missing sessions return empty context. Default limit 40, maximum 2000. Expired sessions return `expired=true` and no context messages.
- `POST /session/get`, `session.read`: `{session_id,owner?}`. Returns full persisted record or 404, including expiry state.
- `POST /session/list`, `session.read`: pagination and owner; returns `{items:[...]}` without history.
- `POST /session/append`, `session.write`: `{session_id,messages,metadata?,expected_revision?,idle_seconds?,owner?}`. Atomically appends messages, merges metadata and increments revision. Empty metadata values remove keys. Revision mismatch or expired session returns 409.
- `POST /session/delete`, `session.write`: `{session_id,owner?}` removes that session.
- `POST /session/expire`, `session.write`: `{session_id,owner?}` marks the existing session expired.

Session output includes messages, metadata, token usage, created/updated/expiry times, `expired` and `revision`. Accepted roles are `system`, `user`, `assistant` and `tool`; tool messages retain tool-call IDs and assistant messages can retain tool calls. At most 200 messages per append, 256 KiB content per message, 64 tool calls per message and 64 KiB arguments per call. History keeps the newest 2000 messages and must remain within 4 MiB serialized. Default idle expiry is seven days; explicit idle seconds must be 0–31536000. Expiry is not automatic deletion. Start a new session rather than appending to an expired one.

## Memory: `memory.store`, `memory.search`

- `POST /memory/get`, `memory.read`: `{key,owner?}`.
- `PUT /memory/put`, `memory.write`: `{key,value,owner?}`, value up to 256 KiB.
- `POST /memory/list`, `memory.read`: pagination and owner.
- `POST /memory/delete`, `memory.write`: `{key,owner?}`.
- `POST /memory/search`, `memory.search`: `{query,limit?,offset?,owner?}`.

Search is bounded lexical matching over stored lowercase text, not embeddings or semantic retrieval. Query must contain 1–512 bytes; at most 16 tokenized terms participate. Skills use the same search mechanism over metadata, paths and file content. Search never spans owners or namespaces.

## Secrets: `secrets.resolve`

This capability contains separate operation scopes; listing does not require resolution permission:

- `PUT /secrets/put`, `secrets.write`: `{key,value,note?,owner?}`. Nonempty value up to 65536 bytes; note up to 4096 bytes. Returns metadata, never value.
- `POST /secrets/list`, `secrets.read`: pagination and owner. Returns only key, note and update time.
- `POST /secrets/resolve`, `secrets.resolve`: `{key,owner?}`. Returns plaintext value only to an authorized runtime caller.
- `POST /secrets/delete`, `secrets.write`: `{key,owner?}`.

A random 256-bit `secrets.key` encrypts values with AES-GCM. Associated data binds namespace, verified owner and secret key. Ciphertext cannot be moved to another owner by relabeling a record. The private key file is not a public descriptor or database field.

Back up `secrets.key` with the database through a consistent, protected backup. Missing key with existing ciphertext fails startup; invalid/wrong keys do not trigger replacement or plaintext fallback. Restore the original key. There is no built-in key rotation or recovery from lost key material.

The `secrets.resolve` capability declares `transport_security: tls` for all its operations. SDK authorization and client transport enforce that generic trait unless `MESH_ALLOW_PLAINTEXT_SENSITIVE=1` explicitly enables the isolated development exception. Signed HTTP is not encryption. Browser `/api/call` forbids `/secrets/resolve`, even for administrators. Store placeholders such as `{{secret:api_key}}` in agent inputs; runtime resolves verified ownership only after tool approval.

## Skills: `skills.search`

- `POST /skills/search`, `skills.search`: query, pagination and owner.
- `POST /skills/list`, `skills.read`: pagination and owner; omits file content.
- `POST /skills/get`, `skills.read`: `{id,version?,owner?}`; omitted version selects latest published package.
- `PUT /skills/put`, `skills.write`: `{id,version,title?,description?,tags?,files,owner?}`, files are `{path,content}`.
- `POST /skills/import`, `skills.write`: package metadata plus base64 `archive` ZIP.
- `POST /skills/versions`, `skills.read`: `{id,limit?,offset?,owner?}`; omits file content.
- `POST /skills/file`, `skills.read`: `{id,version?,path,owner?}`.
- `PUT /skills/file`, `skills.write`: `{id,version?,new_version,path,content,owner?}` publishes a new version with the edited file.
- `POST /skills/file/delete`, `skills.write`: `{id,version?,new_version,path,owner?}` publishes a new version without that file.
- `POST /skills/delete`, `skills.write`: `{id,owner?}` removes latest and entire version history. A version argument is rejected.

Versions are immutable: publishing an existing version returns 409. Every package requires nonempty UTF-8 root `SKILL.md`. Limits: 256 files, 512 KiB per file, 2 MiB total content, 32 tags, 128 bytes per tag, 512-byte title and 16384-byte description. Paths must be normalized relative paths without traversal, backslash, colon or control characters; case-insensitive duplicate paths are rejected. ZIP input is limited to 10 MiB and 512 entries before stricter package limits; symlinks and unsafe entries are rejected. One common enclosing directory can be stripped during import. The 12 MiB JSON cap can be tighter than the ZIP limit after base64 expansion.

Skills are untrusted retrieved data. Importing or publishing a skill does not execute its files or grant tool permissions.

## Workspace and stored artifacts: `workspace.files`

This is a database-backed workspace, not direct access to a host/container filesystem:

- `POST /workspace/create`, `workspace.write`: `{workspace,owner?}`.
- `PUT /workspace/put`, `workspace.write`: `{workspace,path,content,artifact?,owner?}`; creates the workspace if missing.
- `POST /workspace/get`, `workspace.read`: `{workspace,path,artifact?,owner?}`.
- `POST /workspace/list`, `workspace.read`: `{workspace?,path?,artifact?,limit?,offset?,owner?}`. Without workspace, lists workspaces. With workspace, lists matching file metadata; path is a prefix filter.
- `POST /workspace/delete`, `workspace.write`: `{workspace,path?,artifact?,owner?}`. With path, deletes one file in the selected file/artifact class. Without path, removes workspace and all its files/artifacts.

Files use the same confined relative-path rules as skills. Limit: 8 MiB decoded content per file; 4096 files and 64 MiB combined content per owner/workspace, including artifacts. `artifact=true` selects a separate immutable file class: overwriting the same path returns 409, but authorized deletion is supported. Lists omit content. These records do not produce Executor download URLs; see [Executor artifact export](../executor/README.md).

## Events and statistics: `events.write`, `stats.ingest`

- `POST /events`, `events.write`, and `POST /stats`, `stats.ingest`: `{events:[{kind,session_id?,message?,level?,metadata?,usage?}],owner?}`.
- `POST /events/query`, `events.read`: `{after_id?,limit?,kind?,session_id?,owner?}`. Default 50, maximum 200. With positive `after_id`, ascending results follow that cursor; otherwise newest first.
- `GET /events`, `events.read`: default query for the node principal's owner.
- `POST /stats/query`, `stats.read`: `{since?,until?,owner?}`, RFC3339 times.
- `GET /stats`, `stats.read`: default time range for the node principal's owner.

Ingestion allows 1–200 events, kind up to 128 bytes, message up to 65536 bytes, and levels `debug`, `info`, `warn`, `error`. Token counts must be nonnegative and at most 1000000000 per field. Event time is assigned locally. Statistics aggregate the same owner-isolated event table, counting events, message/tool-call kinds, usage totals and up to 100 kinds. Time bounds default from Unix epoch to now; until is exclusive and must follow since, no later than one minute ahead.

Event/stat ingestion is append-only without an automatic retention policy. Sending the same logical event twice is not deduplicated. Generic scanned result batches reject payloads near the 12 MiB response limit; reduce page size. Operators must plan protected database backups and storage capacity rather than assume indefinite bounded disk use.

Related: [security](../../docs/security.md), [agent runtime](../agent/README.md), [troubleshooting](../../docs/troubleshooting.md). Runnable checks for integration owner: `go test ./internal/providers`; this document makes no passing-test claim.
