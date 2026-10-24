# ADR 0007: Treat relay transport as an ordinary capability

Status: Accepted

## Context

Some nodes cannot accept inbound connections across host or network boundaries. Making a public gateway own all business routing would recreate the orchestrator under another name.

## Decision

Direct authenticated HTTP is the default application transport. A relay is an ordinary provider of `mesh.relay`; remote nodes can establish outbound connections through it. Relay forwarding preserves the signed target and original application path. The final provider remains responsible for request authentication and operation authorization.

The relay does not own manifests, select business dependencies, interpret agent messages, or allocate workloads. Deployments choose relay placement and publish reachable addresses. Transport credentials and identity grants remain explicit.

## Consequences

A relay extends reachability without becoming a business control plane. Relay failure affects routes using that relay, not unrelated direct calls. Relays can observe unencrypted traffic; TLS is required when confidentiality is needed. A topology requiring a relay must provision and connect it explicitly; a seed address alone does not bypass a firewall.
