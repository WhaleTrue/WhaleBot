# Single-host deployment

## Default deployment

Requirements: Docker Engine with Compose v2.24+ (Compose v5 also works). The host needs no local Go or Node installation for image builds.

```sh
docker compose up -d --build
docker compose exec web cat /identity/admin-password
```

Open `http://localhost:18000`. Username defaults to `admin`; password is random and persisted in the private web identity volume. Never paste that password into an issue or commit it. Browser sessions are distinct from mesh node principals. Dashboard login does not create a new trusted mesh identity.

The default model is explicitly a deterministic development mock (`LLM_MOCK=1`), not external inference. Configure actual model profiles and disable the mock to use a real OpenAI-compatible upstream. Telegram is optional and remains unconfigured without a token and whitelist. No external key is required to exercise the local mock chat path.

Default nodes contain capability bundles, not protocol roles: agent execution, model access, durable state, optional knowledge and browser access. Bootstrap runs once to provision separate Ed25519 private keys and public trust grants. It exits before business startup and is never involved in discovery or resolution. Only this deployment security prerequisite uses `depends_on`.

The dedicated bridge is `whalemesh_net`. Only the dashboard publishes a default host port, bound to loopback. HTTP in this isolated development bridge is signed but not encrypted. Compose explicitly enables `MESH_ALLOW_PLAINTEXT_SENSITIVE=1`; startup warns about this. Do not reuse that override across hosts or on an untrusted network. Use HTTPS and disable the override there.

## Minimal chat without a dashboard or Executor

```sh
docker compose -f compose.minimal.yml up -d --build
docker compose -f compose.minimal.yml run --rm chat 'Hello WhaleMesh'
```

The terminal adapter discovers `agent.run`; the agent discovers session and LLM capabilities. No central registration service, browser management node, Docker socket or Executor is involved. The session store is a stateful provider; data is not silently moved to another provider after failure.

## Optional capabilities

```sh
docker compose --profile multi-provider up -d
# A second independent model provider with an independent key and data volume.
docker compose --profile telegram up -d
# Configure token and allowed users through the authenticated dashboard.
docker compose --profile relay up -d
# Local relay only; public operation requires TLS/reverse proxy configuration.
docker compose --profile executor up -d
# Grants this one container access to Docker Engine.
docker compose --profile example build hello
docker compose --profile example up -d hello
```

All nodes use normal peer exchange. Docker DNS resolves bootstrap addresses; it does not maintain capability membership. Removing a seed after peers have exchanged does not remove the other peers' local registries.

## Identity and trust changes

`deployment/identities.json` is an administrator-owned public grant plan. It contains no private key. Every identity volume contains only its node's private key and the public trust map. Dynamic children use constrained preprovisioned pools rather than granting themselves authority.

To add a new trusted principal, add a plan entry with narrow `scopes` (invocation permissions) and `provides` (advertisement permissions), add its separate identity volume mounts, rerun bootstrap, and restart consumers of the changed trust map. Merely advertising a capability does not grant access. Do not mount a directory holding all node private keys into a normal container.

```sh
docker compose run --rm bootstrap
docker compose restart
```

Key and data volumes persist across ordinary `down` and `up`. Do not delete volumes unless intentionally discarding identities and application data. Never point two simultaneously running nodes at the same private identity volume as a shortcut for provisioning a replica.
