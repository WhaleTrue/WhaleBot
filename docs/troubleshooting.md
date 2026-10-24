# Troubleshooting WhaleMesh

Use current root Compose files and actual node manifests as the source of truth. Preserve identity/data volumes and unrelated Docker resources. This runbook does not claim any acceptance run passed. [Single-host setup](single-host.md), [multi-host setup](multi-host.md), [security](security.md), and [discovery/resolver](discovery-resolver.md) describe the intended boundaries.

## Collect evidence without exposing secrets

From the repository root:

```sh
docker compose ps -a
docker compose logs --since=10m agent llm store knowledge web
```

Add optional service names only when their profiles are enabled. Record exact error, service, timestamp, trace ID and request ID. Public `/healthz` proves process liveness, not dependency readiness, successful inference or authenticated access. Use dashboard topology for readiness, peers, capabilities, requirements and binding reasons. Node `/mesh/manifest`, `/mesh/peers` and `/mesh/status` require signed `mesh.inspect`; unsigned curl failures are expected.

Do not publish passwords, identity JSON, private keys, secret values, model credentials or whole rendered environments. `docker compose config` and container inspection can expose credentials from overrides. Filter or inspect locally before sharing. Do not run `docker compose down -v`, Docker prune, or delete volumes as a generic repair.

## Dashboard refuses connection or opens the wrong service

Default host endpoint is `http://localhost:18000`; node/container listen port remains 8080. The public dashboard binding is `127.0.0.1:${WEBUI_PORT:-18000}:8080`.

1. Inspect `docker compose ps web` and `docker compose port web 8080` for the actual host mapping.
2. Check an existing `.env`, shell environment, `--env-file` and Compose overrides. Old `WEBUI_PORT`/`RELAY_PORT` values persist; copying a new `.env.example` does not overwrite them. Shell values can override `.env`.
3. Fix the intended port source and recreate the affected service with `docker compose up -d web`. A plain restart does not apply changed port/environment configuration.
4. Do not change `MESH_ADVERTISE` to the browser's host port. Other local containers use `http://web:8080`, not host loopback.

A loopback binding intentionally blocks access from another machine. Configure a protected HTTPS reverse proxy rather than exposing unsigned local HTTP publicly. Relay host port defaults to 18088, not the dashboard port. The remote relay URL must be reachable from the remote host; `localhost` there refers to that remote host.

## Login fails after changing environment credentials

Retrieve the generated password locally:

```sh
docker compose exec web cat /identity/admin-password
```

Never paste this output into an issue. Username defaults to `admin`. Bootstrap credentials initialize only an empty user database. Existing users persist in web data; changing `WEB_ADMIN_PASSWORD` does not reset their passwords. Use the authenticated password-management flow or restore the correct user data and credentials. Restarting web invalidates cookie sessions; log in again. Password changes also revoke existing sessions.

HTTP 403 during mutation can mean missing CSRF token, wrong origin or missing user scope. Browser operation scopes and the web node's trusted mesh scopes are separate. Do not grant `*` to fix one missing operation. See [browser backend](../internal/web/README.md).

## Node keys or trust prevent startup/discovery

Typical errors include `read identity`, `own principal requires matching namespace trust grant`, `own trust public key mismatch`, `untrusted_principal`, `invalid_signature` or an advertisement denied by `provides`.

1. Check the mounted **single-node** identity bundle and `MESH_IDENTITY_FILE`/`MESH_TRUST_FILE` paths. Do not mount a directory containing every node's private key.
2. Match configured namespace and principal to that bundle and its public trust entry. Never rename a principal while retaining an unrelated key.
3. Check file readability by image UID/GID `10001:10001`; private files copied from root-owned directories may be unreadable. Keep restrictive permissions, but assign correct owner/group rather than making keys world-readable.
4. Check `deployment/identities.json`: `scopes` authorizes invocation, `provides` independently authorizes advertisements. Both must describe the intended node, not a legacy role.
5. After an administrator grant change, rerun bootstrap and restart consumers to reload their public trust maps:

```sh
docker compose run --rm bootstrap
docker compose restart
```

Bootstrap preserves provisioned identities; it is not an online registration service. If a key is lost, restore its original protected identity volume. A new key is a new trust/provisioning event, not a transparent restart. Give replicas distinct keys. Membership also rejects more than 16 live non-draining incarnations per principal and a principal's exhausted replay-history share. Investigate accidental key reuse or restart churn; do not delete replay history on a running node to bypass it.

`expired_signature` concerns request clocks/deadlines, not membership TTL. Synchronize host clocks and inspect call deadlines. Membership itself uses origin sequence advancement plus receiver-local TTL, without signed membership timestamps.

## Node is alive but a required capability is missing

Errors include `no ready compatible trusted provider` or readiness reason `required dependency unavailable or cyclic`.

1. Confirm the required provider process exists and its manifest advertises the exact capability, interface and compatible version.
2. Compare namespaces and both sides' trust files. A reachable URL is not a trusted provider.
3. Inspect capability and operation scopes. Explicit requirement scopes must all be granted. A scope union is not a requirement to grant every administrator operation.
4. Check capability `ready`, provider node `ready`, `draining`, local membership expiry, pins, requested attributes and locality constraints.
5. Follow provider requirements recursively. A required dependency cycle cannot become ready. An optional missing dependency alone should not block readiness.
6. Check bootstrap routes from the consuming network. Docker DNS works only on its attached network. A direct local URL is not a valid cross-host route.
7. Let bounded peer exchange receive newer origin sequences. Repeating a stale forwarded announcement does not refresh TTL.

Do not convert required state/model dependencies to optional or report liveness as readiness to hide the failure. A local dependency graph can be ready while a real model upstream rejects credentials or is unavailable.

## Saved sticky binding reports a boundary mismatch

An error such as `sticky binding ... unavailable; explicit policy/pin change required, automatic stateful migration denied` is intentional data safety.

1. Identify the **consumer** node, requirement ID and saved key in its `MESH_DATA/resolver.json`. Do not begin by modifying the provider database.
2. Compare the current provider's namespace, principal, logical name, capability and `state_id` with the saved boundary. Without `state_id`, instance ID is part of the key.
3. Restore the original provider identity/name and data volume when recovering the same data. A new container with an empty volume is not that boundary. Renaming a provider can also change its key.
4. If migration is intended, back up both sides. Stop writes, move/restore data with ownership and secret-key boundaries intact, then explicitly rebind. No automatic copy/merge exists.
5. For a declared requirement, use that consumer's `Node.SetPolicy` integration to choose the new policy/pin and clear its old sticky choice. Browser `PUT /api/policy` affects only requirements owned by the web node, not agent/Telegram/other nodes.
6. For a dynamically generated requirement without a policy API, stop the affected consumer, back up its resolver file, and remove only the intended key from the JSON `sticky` map after making the migration decision. Keep other sticky keys and policy overrides. Restart the consumer. Never erase membership replay state or all application volumes as a shortcut.

Rebinding can expose a different empty store; it does not restore missing history. Persisted files from older architectural snapshots may encode a different boundary key. Handle that as an explicit migration, not automatic fallback. Agent local state, session provider state and web account state are separate volumes.

## Chat has no eligible model or ambiguous paid model

Unspecified model/provider selects only an explicitly free provider. `unknown` cost is not free. Configure a real profile and its cost class honestly; do not label a paid upstream free to bypass the gate.

- Use an exact catalog model ID. Unknown IDs fail instead of selecting another model.
- Supply both `model` and `provider` for paid or unknown-cost models available on multiple providers.
- Provider pin accepts a principal, logical name or canonical escaped `namespace/principal/name/instance_id` key. Bare instance IDs are not selectors.
- Check model feature declarations: a model without tools receives no tools; streaming can be disabled for that profile.
- `LLM_MOCK=1` is explicit development output. It does not prove external inference works. With real mode, upstream errors remain errors.
- Host model servers need a container-reachable address, not container `localhost`. Root local LLM service includes `host.docker.internal` host-gateway mapping; other deployment arrangements need equivalent routing.

`session_busy` means another run for the same owner/session is still active on that runtime. `session revision mismatch` indicates competing writers. `session expired` requires a new session or deliberate data management, not unguarded append. See [agent](../internal/agent/README.md) and [LLM provider](../internal/llm/README.md).

## Tool approval or owner delegation is denied

User text, retrieved skills, model arguments and `approved=true` do not grant authorization. Agent mutations require a separate trusted `/agent/approvals` request for exact tool arguments. Agent approvals expire after ten minutes and are one-use; argument/schema/provider changes require a new approval. Destination policy still applies after that gate.

An external Executor image additionally needs an immutable digest and signed `image.use` approval matching issuer, effective owner, namespace, exact image and expiry. That is not interchangeable with an agent tool approval.

Browser/IM operations need adapter-derived owner plus corresponding node delegation scope: `agent.delegate`, storage `<family>.delegate` or `container.delegate`. Browser user operation scopes are also required. Keep scopes narrow. Do not remove owner injection or restore node-administrator bypass for delegated users.

## Executor cannot access Docker socket: mount GID

Only Executor should mount `/var/run/docker.sock`. The default service is nonroot with supplementary `group_add: ["${DOCKER_GID:-0}"]`.

On a Linux host, inspect socket ownership and container groups:

```sh
stat -c '%u:%g %a' /var/run/docker.sock
docker compose exec executor id
```

Set `DOCKER_GID` in the deployment environment to the reported socket group ID, then recreate Executor:

```sh
docker compose --profile executor up -d executor
```

A restart does not apply `group_add` changes. Docker Desktop's mounted socket ownership can differ from a Linux host; inspect the actual container mount if needed. Do not fix socket permissions with `chmod 666`, run all nodes as root, or mount the socket into agent/web. Socket group access is privileged: restrict who can change Executor configuration.

For HTTPS Engine access, check CA/client files in `DOCKER_CERT_PATH`. Plaintext remote Engine TCP is intentionally rejected. See [Executor](../internal/executor/README.md).

## Deployment fails, disappears or cannot write its volumes

- Missing child identity: provision a distinct bundle at `EXECUTOR_IDENTITIES_DIR/<deployment-id>-<replica-index>`, then update appropriate peer trust. Root Compose mounts only its configured pools.
- `volume ownership mismatch` or unmanaged container: inspect labels. Do not relabel unrelated resources to seize them.
- Initializer failure: inspect exact `volume initializer exit` message. It uses root only for a short-lived, networkless helper over owned named volumes with CHOWN/FOWNER. Final workload remains UID/GID 65532 with all capabilities dropped. Preserve this split; do not run workloads root.
- Data/identity host mounts copied manually must match their intended image user. Root service UID 10001 and dynamic workload UID 65532 are different. Do not recursively chown host directories unrelated to owned volumes.
- Reserved volume path, host bind/port/network, bootstrap environment or oversized resource request: correct deployment to the narrow policy instead of disabling checks.
- `egress=false`: requires an administrator-owned internal Docker network. The flag cannot isolate one child on an already external shared bridge.
- External image: use digest and approval, or a narrowly administrator-approved image. Image-declared Docker volumes are rejected.
- Session/task TTL expiry removes containers **and owned volumes** after inactivity. Global scope does not use idle expiry. `replicas=0` and deployment DELETE also remove owned volumes. Use `stopped` when retention is intended.
- Reconciliation recreates desired resources. Removing a container manually does not change its persisted desired deployment.

## Jobs, files and artifacts fail

Inspect job `state`, `exit_code`, `error`, `stdout` and `stderr`. `complete` means transport execution finished, not that exit code is zero. `GET /executor/jobs?id=...` retrieves one job; it does not list jobs. Image pull jobs use `/executor/images/jobs?id=...`.

Use async jobs for work longer than an individual signed HTTP deadline. A container exec timeout/stream failure may kill the whole container. Restarted jobs become `interrupted`; inspect stored Engine ID and current state before retrying. There is no automatic exactly-once replay. Eight exec/pull jobs can run concurrently; durable index capacity is 2048.

File APIs require a running owned container, helper-compatible Linux architecture and paths within `/workspace`. File content is limited to 8 MiB, directory/export entries to 10000. Export rejects symlinks and special files. Artifact total regular content is limited to 8 MiB; file/export local budget is 60 seconds.

Download URLs are authenticated paths, not public links. Keep the originating provider route; an artifact belongs to its Executor, not any provider with `artifact.export`. Browser download must forward through its authorized backend operation. Artifacts expire after 24 hours; capacity counts 1024 files in the artifact directory, generally metadata plus archive per export. Database workspace artifacts are separate records, not these downloads.

## Secret key loss or TLS-required errors

`secrets.key missing for existing ciphertext; restore original key` is a hard failure. Restore the original provider key with its consistent database backup. Regenerating a key cannot decrypt existing ciphertext. Copying ciphertext to another owner/namespace also fails because AES-GCM associated data binds both.

`tls_required` or `capability operation requires HTTPS` means transport encryption policy is unmet. Sensitive operations declare `transport_security: tls` independently of their capability names. Root Compose's `MESH_ALLOW_PLAINTEXT_SENSITIVE=1` is an explicit trusted-development-bridge exception, not a cross-host fix. Browser raw `/secrets/resolve` is forbidden even over HTTPS; runtime resolves placeholders after authorization.

## Relay connects poorly or TLS forwarding fails

1. Confirm a remote node can reach the public relay name and correct port. A loopback-only root mapping or Docker-only DNS name is not a WAN endpoint.
2. Check certificate SAN/hostname, expiry and CA trust. Use `MESH_CA_FILE` for a private CA; never disable verification. Native TLS needs both cert/key and an HTTPS advertisement.
3. Check both trust maps and `mesh.peer`/`mesh.relay` grants. Destination operation permissions remain separately required.
4. Ensure reverse proxy preserves HTTP/1.1 Upgrade, signed headers/body, exact application path/query, `/mesh/*`, `/relay/connect` and `/relay/forward/*`.
5. Match advertised route principal/instance to current outbound session. `relay_unavailable` means hub has no live session for that target; `relay_transport` means forwarding failed.
6. Observe reconnect logs. Backoff grows to 30 seconds. Relay restart drops active streams; the remote node reconnects, then membership converges.
7. For secret routes, protect both TLS links and inspect where TLS actually terminates. A tunneled verified TLS connection supplies destination TLS state; an unrelated plaintext direct hop does not. A relay is a trusted hop, not end-to-end payload encryption.

The SDK chooses another advertised route on later calls after failures; it does not safely replay an ambiguous mutation. Restore transport and inspect operation outcome before retrying. A relay outage is not a reason to migrate state. See [relay component](../internal/relay/README.md).

## Verification versus claims

Run repository checks and acceptance only when authorized by the integration workflow. Unit checks, development mock chat, simulated benchmark cases and local isolated Docker networks prove different things. None proves real external-model credentials, Telegram delivery, public TLS/NAT or physical multi-host operation. Report the exact exercised command and observed result; keep failures visible.
