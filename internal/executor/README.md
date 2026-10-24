# Executor

Executor is the only package that talks to Docker Engine. Ordinary nodes never receive the Docker socket. `RegisterContext` registers capabilities and a lifecycle worker; `New` accepts explicit Engine/options for embedding. This is a constrained Mesh Node deployment boundary, not an arbitrary Docker API proxy.

## Installation and privileges

Enable root Compose profile `executor`. The service runs as image user `10001:10001`, not root. Compose adds supplementary group `${DOCKER_GID:-0}` for the socket mount. On Linux, set `DOCKER_GID` to the actual socket group ID; restarting with a different primary user is not the fix. Access to the socket remains host-privileged access even though this process is nonroot.

Required configuration:

- `EXECUTOR_NETWORK`: administrator-configured dedicated Docker network; root Compose uses `whalemesh_net`.
- `EXECUTOR_NODE_IMAGE`: trusted image containing `/app/node` and the file/volume helper.
- `EXECUTOR_IDENTITIES_DIR`: read-only pool of preprovisioned child identities, required when deploying children.
- `EXECUTOR_APPROVAL_KEYS_FILE`: optional JSON map of issuer names to base64 Ed25519 public keys.
- `EXECUTOR_ALLOWED_IMAGES`: administrator-owned comma-separated image allowlist.

`DOCKER_HOST` defaults to `unix:///var/run/docker.sock`. HTTPS Engine transport requires `DOCKER_CERT_PATH` containing `ca.pem`, `cert.pem` and `key.pem`. Plaintext TCP is rejected except literal loopback addresses with `EXECUTOR_ALLOW_LOCAL_TCP=true`, intended for controlled testing. Redirects never forward Engine credentials.

Persistent files live under `dataDir/executor`: `state.json`, job output files and exported artifacts. Keep this volume and the Executor principal stable. State writes use private files and atomic rename.

## Ownership boundary

Every managed resource carries `io.whalemesh.managed=true`, matching executor principal, public-key fingerprint, namespace and owner labels. Deployment, replica, revision, scope, session and task labels add lifecycle context. Knowing an ID cannot make unmanaged resources or a different signing authority's resources manageable.

An absent owner becomes `node:<authenticated-principal>`. Explicit owner requires `container.delegate`; trusted adapters derive it from their users. Delegated requests disable the node-level `executor.admin` ownership bypass. An undelegated node administrator can cross owners only within this Executor's managed namespace, never into unrelated Docker resources. `host_id`, container name and caller-supplied labels are not ownership evidence.

## HTTP operations

All routes require signed mesh authentication. Browser callers use the operation names advertised in topology through `/api/call`, not an unsigned request to these paths. Capability scopes below are also the operation scopes:

- `container.manage`: `GET /executor/containers`, `POST /executor/containers/action`, `GET /executor/logs`.
- `container.exec`: `POST /executor/exec`, `GET /executor/jobs`.
- `container.files`: `POST /executor/files`.
- `artifact.export`: `POST /executor/artifacts/export`, `GET /executor/artifacts/download`.
- `container.images`: `GET /executor/images`, `POST /executor/images/pull`, `GET /executor/images/jobs`.
- `deployment.reconcile`: `GET`, `PUT`, `DELETE /executor/deployments`.
- `executor.admin`: `POST`, `DELETE /executor/approvals`. These administrator routes are not exported as a separate business capability.

General JSON decoding rejects unknown fields and multiple values, with a local 24 MiB reader bound. The shared signed HTTP layer is stricter at 16 MiB. Deployment documents are stricter again at 1 MiB. Policy/Engine errors generally use HTTP 400 `executor_error`; inspect the message rather than assuming every rejected resource is a missing container.

Every capability declares input/output JSON Schemas for its primary endpoint: container listing, execution, workspace files, artifact export, image listing or deployment listing. These describe primary success payloads, not every heterogeneous secondary operation or error response. Deployment PUT still uses the complete strict deployment schema linked below.

## Deployment API

`PUT /executor/deployments?owner=...` accepts the strict `whalemesh.deployment/v1` object. Schema: [deployment.schema.json](../../schemas/deployment.schema.json); Go contract: [schema.go](../../pkg/mesh/schema.go). Fields include `node`, capability `requires`, image, command argv, environment, secret references, logical volumes, resources, network, replicas, scope, session/task IDs, TTL, restart policy, desired state, labels and approval IDs. `node` has required `name` and optional `labels`, `annotations`, `requires` only. Namespace stays on the deployment. There is no `manifest` field. Caller-supplied principal, instance, host, security, credentials, transports and provides are rejected, not ignored. Do not add an `owner` property to the deployment document; delegation uses the query.

`GET /executor/deployments?owner=...` returns authorized desired records, including persisted reconciliation error. `DELETE /executor/deployments?id=<deployment>&owner=...` requests `absent`, removes owned containers and removes owned named volumes. It is destructive, not merely a pause. `replicas=0` also removes containers and owned volumes. Use `desired_state=stopped` to retain them.

Policy is deliberately narrower than Docker and, in places, narrower than the schema:

- Same namespace as Executor; deployment ID uses lowercase letters, digits, hyphen or underscore, at most 63 characters.
- 0–16 replicas. Each needs a distinct preprovisioned principal from `<identities-dir>/<deployment-id>-<replica-index>/identity.json` plus `trust.json`. Executor's own key cannot be reused. Each assigned private key must match its namespace-bound trust grant. This pool is explicit operator authorization, not a runtime certificate issuer.
- Desired state is `running`, `stopped` or `absent`. Restart policy is `no`, `on-failure` or `unless-stopped`.
- Scope is `global`, `session` or `task`. Session/task scope requires the corresponding ID and positive TTL, at most 604800 seconds. Global scope cannot include session/task IDs.
- `node.name` is the logical node name. `node.labels` and `node.annotations` each allow at most 64 public string entries, keys 1–128 characters and values at most 4096 characters. Credential material is denied. The image alone implements and publishes capabilities; deployment has no package selector.
- `node.requires` and deployment `requires` use the SDK Requirement contract. Executor concatenates both into private child configuration, at most 256 combined entries; nonempty requirement IDs must be unique across both lists. Requirement scopes are checked against the assigned child's trust grant, never the Executor's own scopes. Image defaults still fill IDs not explicitly configured by deployment.
- Executor writes private `manifest.json` with namespace, logical name, host, public labels/annotations and merged requirements into the child identity volume. `MESH_MANIFEST` uses bounded, duplicate-free, unknown-field-denying configuration decoding; it is not public `DecodeManifest`. SDK startup loads the assigned credentials, creates a fresh instance and fills trusted security/lifecycle/transport defaults before validating the running manifest. Executor sets listener, advertise address and seeds; public running-manifest decoding remains strict.
- Reserved `MESH_*`, `NODE_*`, `EXECUTOR_*` and `DOCKER_HOST` bootstrap overrides are denied. Environment is public deployment data, not a secret channel.
- CPU, memory and PID limits are mandatory in schema. Minimums are 1000000 NanoCPUs, 16 MiB and one PID. Default Executor ceilings are 2000000000 NanoCPUs, 2 GiB and 256 PIDs; embedding options can change ceilings.
- Only the configured mesh network is allowed. No host port publication. `egress=false` requires that network to be administrator-provisioned as Docker `Internal=true`; it does not dynamically firewall a shared external network.
- Volumes are logical named volumes, not bind mounts. Executor hashes names and verifies labels before reuse. Maximum 32 user volumes. Reserved paths include `/run`, `/proc`, `/sys`, `/dev`, `/tmp`, `/workspace`, `/app`, `/identity`, `/data` and their descendants.
- At most 64 secrets; each target is `/run/secrets/<safe-filename>`. Secret descriptors contain references only. Values resolve under the deployment's verified owner through `secrets.resolve`.
- Only Linux images without image-declared volumes are accepted. Workloads need compatible helper execution for file APIs. An allowed arbitrary image is not guaranteed to act as a functioning Mesh Node.

The trusted node image and exact administrator allowlist entries bypass external-image approval. All other images require an immutable `@sha256:` reference and independently signed `image.use` approval. Do not expand the allowlist to bypass an unexplained approval failure.

A desired record persists before immediate reconciliation, which has a two-minute local budget. The lifecycle worker retries reconciliation every 30 seconds, also under a two-minute batch budget. Failed replacement creation attempts to restart the previous replica. This is not transactional rollout or a zero-downtime guarantee.

## Workload and initializer isolation

Each replica receives owned workspace, identity, secrets and data named volumes, plus approved user volumes. A short-lived initializer uses only the trusted node image, UID 0, no network, read-only root, all capabilities dropped except **CHOWN and FOWNER**, no-new-privileges, 64 MiB memory, 0.1 CPU and 16 PIDs. It prepares only the owned mounted volumes, refreshes identity/secret files and is then removed. Root initialization is not workload root execution.

The final workload runs UID/GID `65532:65532`, read-only root, `CapDrop=ALL`, no-new-privileges, bounded resources and no privileged mode. Identity and secret mounts become read-only. `/tmp` is a 64 MiB `nosuid,nodev` tmpfs. Docker log rotation is 10 MiB times two files. Workspace/data remain owned writable volumes. Dynamic child network seeds point to the Executor's advertised mesh address, not a global coordinator.

All named mounts use Docker `VolumeOptions.NoCopy`: image copy-up must not replace initialized UID/GID 65532 ownership. The initializer installs the trusted file helper in `/run/whalemesh/whalemesh-files` before that volume becomes read-only. Per-operation request files use the private writable `/data` volume and are consumed/deleted by the helper; Docker archive writes to the read-only rootfs's `/tmp` tmpfs are not used. Workspace APIs remain confined to `/workspace`, outside these private paths.

Session/task deployments expire after inactivity: deployment updates, `touch`, successful actions, execution and file/export operations refresh activity. Queued/running execution jobs prevent idle cleanup. Global deployments do not expire through this TTL mechanism. Expiry requests absence and deletes owned resources; it does not archive their data.

## Image approvals

`POST /executor/approvals` accepts `{id,issuer,subject,namespace,action,resource,expires_at,signature}`. Action must be `image.use`; resource is the exact immutable image reference; subject is the exact effective owner. Signature is base64 Ed25519 over `ApprovalPayload`, JSON with the signature field omitted. Issuer must exist in the configured public key map. Expiry, namespace, subject and resource are rechecked at use. Existing IDs cannot be rebound to different claims. `DELETE ...?id=...` revokes a stored approval.

Browser `POST /api/container/approval` separately checks user `executor.admin`, signs a one-hour owner-specific approval and submits it using trusted web identity. Executor must trust that issuer key. An agent tool approval is a different gate and does not authorize an external image.

## Execution and durable jobs

`POST /executor/exec` accepts `{id,command,cwd?,env?,timeout_seconds?,async?,owner?}`. `command` is explicit argv; no shell is inserted. `cwd` is confined to `/workspace`, with empty path meaning workspace root. Execution uses UID/GID 65532, never requested root.

Timeout defaults to 300 seconds, accepts 1–7200. Individual synchronous SDK requests are capped at five minutes, so use async for longer work. `async=true` returns HTTP 202; otherwise the response contains the completed job. A job's `state=complete` does **not** imply exit code zero: inspect `exit_code`, `error`, `stdout` and `stderr`.

Jobs move through `queued`, `running`, `complete`, `failed` or restart-time `interrupted`. `GET /executor/jobs?id=<job>&owner=...` retrieves one authorized job; it is not a list endpoint. Pull requests use `{image,approval_ids,owner?}`, always return a job with HTTP 202, and have a 30-minute local budget. Retrieve pull jobs through `/executor/images/jobs?id=...`.

Limits: eight simultaneous exec/pull jobs combined, 2048 retained durable job index entries, 4 MiB captured command output. Container logs return at most the latest 1000 lines within the same 4 MiB frame bound. Completed job index entries expire after seven days. Output JSON files are separate; no file-pruning guarantee is implemented. Plan disk maintenance accordingly.

Before starting exec, Engine exec ID is persisted. Restart marks queued/running jobs interrupted; background recovery inspects known Engine IDs and kills a still-running owned container to prevent orphan execution. Lost IDs cannot establish outcome. Timeout, cancellation or stream failure can also kill the **whole container**, not only its child process. Inspect container state and actual effects before retrying. There is no exactly-once replay.

## Files and exported artifacts

`POST /executor/files` accepts `{id,operation,path,to?,content_base64?,owner?}`. Operations: `list`, `read`, `write`, `mkdir`, `move`, `delete`. Paths are relative to `/workspace` or explicitly under it. The helper uses Go `os.Root` confinement, rejects escape paths and refuses root overwrite/move/delete. There is no host filesystem route. Writes use a synced temporary file and rename. Limit: 8 MiB decoded file content, 10000 directory entries, 60-second local file operation budget. Helper requests/output have additional bounded limits; executable must match workload OS/architecture.

`POST /executor/artifacts/export` accepts `{id,path,owner?}` and returns HTTP 201 `{id,name,url,media_type,owner,created_at,expires_at}`. It creates gzip-compressed tar from the confined workspace path, rejects symlinks/special files, and limits total regular content to 8 MiB and traversal to 10000 entries. File/export calls require a running workload that can execute the helper.

Artifact URLs are authenticated application paths, not public bearer links. URLs include provider principal so adapters can route to the originating Executor. `GET /executor/artifacts/download?id=...&owner=...` checks owner/admin and expiry; `provider` is routing metadata, not ownership permission. Artifacts expire after 24 hours; the worker removes expired files. Capacity checks count 1024 directory entries, normally two per export, not 1024 guaranteed exports. Artifacts live in Executor data independently of later workload removal, until expiry.

This export is separate from immutable database artifacts offered by [workspace providers](../providers/README.md). See [troubleshooting](../../docs/troubleshooting.md) and [security](../../docs/security.md). Integration owner checks: `go test ./internal/executor` and `python scripts/acceptance.py`; no result is asserted here.
