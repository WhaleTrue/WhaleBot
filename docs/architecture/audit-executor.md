# Executor legacy audit

Graph query: `graphify query "docker creator userdocker APIs container exec file artifact deployment"` traversed the creator and userdocker implementations before source inspection.

## Observed implementation

- `user-docker-manager/internal/creator/creator.go` implements Docker HTTP calls over `/var/run/docker.sock`, lifecycle operations, logs, image discovery and asynchronous pull state.
- Creation accepts a caller-controlled `external_image_approved_by_user` boolean. Workspace strings become Docker bind specifications without a host-path boundary. Caller labels are copied before framework labels are applied.
- Creation hardcodes `userdocker.v1`, container port 9000, orchestrator registration environment variables and HTTP contract probing. Images must expose the old userdocker interface even when their actual capability differs.
- Only session and global scopes exist. Idle activity and pull jobs live in process memory; restart loses recent activity and job state. Scope switching removes the old container before replacement succeeds.
- `user-docker-manager/cmd/server/main.go` exposes lifecycle, image, cross-container copy and userdocker proxy APIs. Idle cleanup removes session containers but leaves their workspace volumes.
- `userdocker-base/main.go` uses local `os/exec`, optional `sh -c`, memory-only asynchronous execution, local filesystem operations and tar.gz export. `resolveWorkspacePath` checks lexical containment, not symlink containment. File and command outputs are buffered without a durable bound.

## Rewrite boundary

`internal/executor` alone talks to Docker Engine. Mesh trust scopes authorize every operation. Executor labels bind resources to namespace, executor principal, requesting principal and deployment. Desired deployments, signed administrator approvals, activity and asynchronous job records persist atomically. Containers run normal Mesh Node identities and manifests; no userdocker contract, orchestrator registration or shell dependency survives. Defaults prohibit host binds, Docker sockets, host networking and privileged containers. Files use a root-confined helper in the target container; Docker archive is transport only, never a caller-controlled host path.

The root entrypoint must dispatch the executor file helper before normal node startup. Dynamic children need explicit persistent identities and trust grants; the executor must never mint authority that its configured issuer does not hold. Docker Desktop can be reached through its Unix socket from the executor container or an explicitly enabled loopback Engine TCP endpoint during local tests.
