# ADR 0006: Resolve compatible interfaces locally

Status: Accepted

## Context

Selecting the first provider with a matching capability name can bind incompatible APIs, cross a namespace boundary, or move mutable session state between independent stores.

## Decision

Requirements declare capability ID, interface, semantic-version constraint, namespace, locality, selection policy, pin, optional/many/sticky flags, attributes, and scopes. Interface identity is an exact match. Versions support exact SemVer, `*`, caret, tilde, and comparison conjunctions. A prerelease only matches a constraint that explicitly names the same prerelease release line.

The SDK filters trusted, ready providers before applying local selection policy. Pinning and sticky bindings make provider ownership explicit. Stateful stores remain independently owned; binding failover is not a data replication protocol. Callers must distinguish dependency absence from provider execution failure.

## Consequences

Changing an interface incompatibly requires a new major version or interface identifier. A broad version constraint is a consumer decision, not permission to ignore input/output contracts. A missing sticky state provider must not silently become an empty replacement store. Local policy requires no cluster leader and can differ between callers.
