# ADR 0008: Isolate Docker behind executor-owned desired state

Status: Accepted

## Context

The legacy execution request includes a user approval boolean and loose Docker options. Such a request cannot prove user approval or safely express a complete workload lifecycle.

## Decision

Only the executor owns Docker Engine access. Other nodes call ordinary `container.manage`, `container.exec`, `container.files`, and `artifact.export` capabilities. Deployment desired state includes a minimal node template (logical name, public labels/annotations and requirements), image, command, environment, secret references, named volumes, resources, network, replicas, scope, lifetime, restart policy, desired state, labels and approval references. It does not embed a running Node Manifest. The executor assigns trusted identity and transport; the image implements and publishes its actual capabilities.

The schema exposes no privileged mode, host namespace, device, host bind, capability-add, or raw Engine payload. Environment cannot override mesh identity configuration. Named volumes cannot hide identity or system paths. The executor applies narrower ownership, image, resource, network, and identity-allocation policy after schema validation. Approval references must resolve to independently verified cryptographic grants; booleans cannot authorize execution.

## Consequences

A schema-valid deployment may still be denied by execution policy. Docker errors remain visible. Session and task workloads require bounded lifetimes. Global workloads are explicit. Executor-owned labels prevent unrelated containers from entering management scope. Docker socket access is never a feature of the SDK or hello provider.
