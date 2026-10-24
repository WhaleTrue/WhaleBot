# ADR 0001: Use an autonomous capability mesh

Status: Accepted

## Context

The legacy orchestrator registry and reverse proxies make discovery and business calls depend on one central service. Moving that service to another container does not remove the dependency.

## Decision

Every process uses `pkg/mesh.Node`. A node advertises provided capabilities and declares required interfaces. Callers resolve providers from their own verified membership view and call providers directly. Seed addresses bootstrap discovery; seeds do not own membership or dispatch requests. There is no compatibility layer for old registration APIs or component roles.

The universal executable composes ordinary packages through `NODE_PACKAGES`. Package names select code at startup; they are not SDK node roles. A process can provide and consume multiple capabilities.

## Consequences

A seed outage does not invalidate already discovered providers. Partitions produce different local views and explicit unavailable dependencies rather than a fictional globally consistent registry. Deployments must provide at least one reachable initial path between disconnected groups. No node is required to serve as a central orchestrator.
