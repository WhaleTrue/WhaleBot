# ADR 0010: Make extensions ordinary SDK providers

Status: Accepted

## Context

A plugin interface that requires a special container role, central registration route, or Docker access would create a second architecture for user-built capabilities.

## Decision

An extension uses the public `pkg/mesh` SDK, loads provisioned identity and trust, creates a node, registers authenticated HTTP handlers, advertises capabilities, and starts the node. Capability declarations include interface version, operation semantics, scopes, input/output schemas, and optional model tool metadata. A tool export must reference a declared operation and cannot add undeclared scopes.

`examples/hello` is a runnable executable with capability and scope `hello.invoke`. It uses the ordinary SDK client/server path, has no Docker dependency, and does not generate credentials. Its checks cover greeting validation, contract compilation, signed invocation, and unsigned rejection.

## Consequences

New capabilities require no SDK role enum or orchestrator modification. Extensions still need administrator-issued advertisement and invocation grants. Tool discovery is descriptive: it does not authorize invocation or remove confirmation policy. The same executable can run directly or in an executor-managed container, subject to deployment policy.
