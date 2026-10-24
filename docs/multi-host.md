# Multi-host mesh and Relay

Docker bridge networks with identical names on different hosts do not communicate. Docker DNS is local to the attached network. A remote node needs a reachable bootstrap/transport path; naming a bridge or seed cannot bypass NAT or a firewall.

## Transport model

The SDK uses the same capability binding and HTTP client for local direct, routed HTTPS and relayed calls. A relay node advertises `mesh.relay` and accepts authenticated outbound HTTP/1.1 Upgrade connections. Yamux carries ordinary HTTP streams back to the dialing node. The original application request remains signed for its final principal and instance; both relay policy and destination policy are checked.

A relay chooses no business provider, owns no session data and stores no global membership database. Peers still exchange signed origin announcements and resolve dependencies locally. Losing a relay affects routes through it, not unrelated direct calls.

## Provision the remote identity

Default bootstrap provisions a constrained `remote-model` principal with model and relay advertisement permissions, not Docker or secret administrator rights. Export only this identity bundle, never all local node private keys:

```sh
docker compose up --build bootstrap
mkdir -p .secrets
docker compose cp bootstrap:/identities/remote-model .secrets/remote-model
```

This creates a stopped bootstrap service container so `compose cp` can select it. Transfer only the exported directory securely to `.secrets/remote-model` beside the repository on the remote host. It contains `identity.json`, `trust.json` and public approval keys. Do not commit it. Copied host ownership need not match the original volume ownership: the remote container must read this directory as UID 10001. On ordinary Linux Docker, run `sudo chown -R 10001:10001 .secrets/remote-model` and `sudo chmod -R go-rwx .secrets/remote-model` on the remote host. Rootless/user-namespace deployments must use the corresponding mapped host UID instead.

Adding another remote principal requires a new public grant-plan entry, a distinct private identity volume and redistribution/reload of the public trust maps. Do not copy one key to unrelated replicas as a substitute for provisioning.

## Expose a verified relay endpoint

Start the local relay:

```sh
docker compose --profile relay up -d
```

This default publishes only loopback HTTP for development. It is not a public multi-host configuration. Put a TLS reverse proxy in front of it or configure native SDK TLS. A reverse proxy must:

1. Present a valid certificate for the public relay DNS name.
2. Preserve path/query and HTTP/1.1 Upgrade for `/relay/connect`.
3. Forward `/mesh/*` discovery endpoints and `/relay/forward/*` without changing the signed application data.
4. Reach the relay only on a trusted local interface/bridge.

For native TLS, mount a certificate/key into the relay and set `MESH_TLS_CERT`, `MESH_TLS_KEY`, `MESH_CA_FILE` as needed, plus an HTTPS advertised address. Adjust its local Docker healthcheck to the TLS listener and certificate hostname. Do not disable application certificate verification to make a healthcheck pass.

Use public DNS or explicit routing that is reachable from all consuming nodes. Split DNS or hairpin NAT may be necessary for local peers to use a remote node's public relay route. Namespace and trusted signing keys, not IP address or HostID, remain the authorization boundary.

## Start the outbound-only remote node

On the remote machine, after installing its identity bundle:

```sh
REMOTE_HOST_ID=gpu-host-1 \
MESH_REMOTE_RELAY=https://relay.example.net \
docker compose -f compose.remote.yml up -d --build
```

`compose.remote.yml` publishes no port. `NODE_PACKAGES=llm,relay` embeds the ordinary model provider plus transport. `MESH_RELAYS` and bootstrap seeds point at the reachable relay. The remote node leaves direct advertised addresses unset. Relay registration adds routes of this form:

```text
https://relay.example.net/relay/forward/<principal>/<instance>
```

Default remote output remains explicitly mocked until real model profiles are configured and `LLM_MOCK=0`. A remote model node is not a privileged executor. Change its capability bundle and grants explicitly if it must provide other capabilities.

## Recovery and multiple routes

The dialing node reconnects with bounded backoff. Relay connection replacement closes stale sessions conditionally, so an old disconnect cannot remove a newer connection. Relay restart drops streams; the remote node reconnects and peer exchange restores current membership. Data ownership remains at the relevant stateful provider.

Comma-separated `MESH_RELAYS` can publish multiple relay routes. The SDK remembers transport failures and prefers another reachable advertised address on later calls. It does not replay side-effecting requests after an ambiguous failure. Inspect current state before retrying a mutation; reconnection does not provide exactly-once execution.

## Reproducible local multi-network test

`deployment/acceptance.yml` puts `remote-model` on a separate bridge with no direct mesh-network attachment. Only the relay attaches to both networks. `scripts/acceptance.py` invokes its capability and restarts the relay, proving the outbound data path and recovery in real Docker.

This is a network-isolation integration test on one Docker Engine, not a claim of physical multi-host/WAN verification. Public certificates, NAT/firewall behavior, bandwidth and operator-specific proxies still need deployment-specific testing.
