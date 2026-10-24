# Schema and developer-contract audit

Graph query: `graphify query "Node Capability Deployment schemas validation hello example architecture ADR"`.

## Evidence

- `orchestrator/internal/nodes/hub.go:27-33` models registration as a node name, a version string, capability names, and arbitrary metadata. The connection handler checks a shared token and node-name syntax, but does not validate capability interfaces, version constraints, or a formal manifest.
- `user-docker-manager/internal/creator/creator.go:25-39` accepts image, command, environment, labels, network, scope, session, workspace, and a caller-controlled external-image approval boolean. It does not represent complete desired state, replica count, resource limits, task scope, secret references, or a signed approval reference.
- `user-docker-manager/internal/creator/creator.go:74-105` describes the legacy `userdocker.v1` contract with service types and fixed introspection endpoints. This couples execution permission to one container role rather than a normal capability manifest.
- `README.md:108` leaves component schemas as roadmap work. The repository has no `schemas/` or ordinary SDK hello example at audit time.

## Cutover

WhaleMesh uses closed Draft 2020-12 schemas for node manifests, capabilities, and deployment desired state. Go entry points apply those schemas and semantic checks. Registration identity, transport, permissions, lifecycle, operation semantics, and requirement compatibility become explicit. Secret bytes and private keys never belong in public manifests. Deployment secrets are references resolved only by an authorized executor. User-supplied booleans do not grant execution approval.

The hello example uses the same SDK and signed request path as every other provider. Architecture decisions document actual contracts rather than a role hierarchy. This audit does not claim tests or runtime verification.
