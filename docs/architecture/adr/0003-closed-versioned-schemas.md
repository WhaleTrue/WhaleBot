# ADR 0003: Validate closed Draft 2020-12 contracts

Status: Accepted

## Context

Arbitrary metadata and capability-name lists do not establish a callable interface. Silent acceptance of unknown fields makes a misspelled safety option look effective.

## Decision

`schemas/` contains closed JSON Schema Draft 2020-12 documents for node manifests, capabilities, and deployments. The wire versions are `whalemesh.node/v1`, `whalemesh.capability/v1`, and `whalemesh.deployment/v1`. Go embeds these documents and compiles them with `github.com/santhosh-tekuri/jsonschema/v6`.

Raw decoders reject unknown fields, duplicate object keys, trailing JSON, documents above 1 MiB, and nesting above 64 levels. Semantic validators additionally enforce identity consistency, safe endpoints and mounts, version constraints, operation declarations, and public-manifest secret policy. Provider input and output schemas use Draft 2020-12; schema compilation cannot fetch external resources.

## Consequences

Unknown versions fail rather than downgrade. Adding a wire field requires a deliberate schema revision and synchronized producer/consumer change. Go validation supplements JSON Schema where a condition spans fields or concerns execution policy. Schema acceptance does not itself grant authorization.
