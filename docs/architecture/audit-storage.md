# Storage capability audit

## Evidence

Before source exploration, `graphify query` located storage and caller symbols for sessions, memory, secrets, skills, workspace, events and statistics. A second graph query located runtime event emission. Source review covered `session/internal/store/store.go`, session HTTP handlers, `memory/cmd/server/main.go`, skills store/types/index/path/ZIP code, `workspace/cmd/server/main.go`, `logger/cmd/server/main.go`, `stats/internal/store/store.go`, stats handlers, and runtime catalog/event callers.

## Existing implementation

- Sessions maintain an in-memory map backed by SQLite JSON rows. A process mutex serializes append. Idle expiration prevents further append and hides expired context. History is truncated to a configurable message count. Persistence failures are discarded by append, clear and expiration. Session identifiers have no principal or namespace boundary. Metadata, optimistic concurrency, durable tool-call context and cumulative usage are missing.
- Notes use SQLite key/value upsert, without search, deletion or ownership. Secrets use AES-GCM and bind ciphertext to key names, but not namespace/principal. Invalid key files regenerate keys and delete secrets. Listing decrypts secrets and exposes partial values. Requests are not authenticated and JSON decoding has no size/unknown-field limit.
- Skills store packages on disk and maintain a separate SQLite search index. CRUD, text file editing, ZIP import and full-text search exist. Paths and file/package sizes have checks, but ZIP extraction does not explicitly reject symlinks and duplicate paths. Filesystem mutations and index changes are not one transaction. Packages have no principal isolation or immutable version history. Search retains a legacy duplicate body field.
- Workspace only lists directory names and creates directories. Caller names reach `filepath.Join` without a confined path contract. There is no user isolation, file CRUD, artifact retrieval or quota.
- Logger writes individual SQLite events and reads recent events without ownership. Statistics ingest batches and aggregate lifetime/24-hour token and event counts. Logger and statistics are separate unauthenticated HTTP services. Runtime discovers them through an orchestrator and emits best-effort HTTP calls.

## Rewrite decisions

`internal/providers` registers ordinary mesh capabilities. There is no registration client, orchestrator route or old API alias. SQLite is the source of truth for all records and binary files, so package updates, session changes and quota checks commit atomically. WAL, full synchronization and busy timeout provide durable local storage. Namespace and authenticated principal are part of every record key. Explicit delegated-user scopes permit trusted web/runtime nodes to address verified user ownership; untrusted body ownership is rejected.

Sessions expose bounded history/context, durable expiry, metadata, usage and revisions. Expired sessions cannot silently restart. Secrets use AES-256-GCM with namespace/owner/key associated data; invalid or missing keys for existing ciphertext stop startup instead of deleting data. Skills keep immutable versions, secure ZIP import and searchable package text. Workspace files/artifacts remain isolated database objects, not host paths. Event queries and statistics share durable per-owner ingestion. Resource limits are documented in the component README. Local state remains sticky: provider manifests advertise their storage boundary, and consumers must retain the selected provider for existing sessions.

## Verification

Storage behavior checks use `go test ./internal/providers`; real owner-isolation and lifecycle integration uses `python scripts/acceptance.py`.
