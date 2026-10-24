# Ordinary SDK hello provider

`go run ./examples/hello` runs a real `pkg/mesh` provider. It advertises `hello.invoke` with interface `hello.invoke`, version `1.0.0`, operation `POST /hello`, and scope `hello.invoke`. Input is `{"name":"WhaleMesh"}`; output is `{"message":"Hello, WhaleMesh!"}`. The operation is read-only and idempotent. Its optional model tool export uses the same declared operation and scope.

The executable never opens Docker, calls an orchestrator, invents credentials, or accepts unsigned business calls.

## Standalone development

Provision separate provider and client keys:

```sh
go run ./cmd/bootstrap -plan examples/hello/identities.json -root .local/hello-identities
```

Start the provider with these environment variables. This shell example targets a local Linux/macOS shell; set equivalent environment variables in PowerShell on Windows.

```sh
MESH_NAME=hello \
MESH_NAMESPACE=default \
MESH_LISTEN=127.0.0.1:18081 \
MESH_ADVERTISE=http://127.0.0.1:18081 \
MESH_DATA=.local/hello-data \
MESH_IDENTITY_FILE=.local/hello-identities/hello-0/identity.json \
MESH_TRUST_FILE=.local/hello-identities/hello-0/trust.json \
go run ./examples/hello
```

`GET http://127.0.0.1:18081/healthz` exposes liveness only. An unsigned `POST /hello` must be rejected. Use a node with the `hello-client` identity and seed `http://127.0.0.1:18081` to discover and call the capability through `Node.Resolve` and `Node.Call`. The SDK signs the call. Do not copy the provider private key into the client.

Keep `.local/hello-identities` private and outside source control. Do not run the same principal concurrently in this standalone example and a mesh deployment. For cross-container calls, advertised addresses must be reachable from the caller; a host-local loopback URL is not a Docker-network address.

## Executor-managed deployment

Build the ordinary example image:

```sh
docker build --target hello -t whalemesh/hello:local .
```

`deployment.json` is the full desired-state document for `PUT /executor/deployments` on a resolved `deployment.reconcile` provider. Send it through the SDK or the authorized browser backend, not an unsigned curl request. The caller needs the declared operation scope. Default deployment provisioning supplies identity pool entry `hello-0`, image allowlist entry `whalemesh/hello:local`, and Docker network `whalemesh_net`.

The deployment contains `node: {name, labels, annotations, requires}` and a separate `namespace`; it contains no running manifest, identity, credentials, transport or advertised capabilities. Executor selects the preprovisioned child principal and host. SDK startup creates a fresh instance and publishes the capabilities implemented by the image. The command is explicit argv `[/app/hello]`, not a shell command. The task deployment requests one replica, 64 MiB memory, 0.1 CPU, 64 processes, and a 300-second task lifetime. It publishes no host ports and supplies no secrets or host mounts.

Both `node.requires` and top-level `requires` accept typed SDK capability requirements. Executor concatenates both lists into the private child configuration; named requirements must have unique IDs across both lists. The image adds its default requirements only for IDs not configured by the deployment. Labels and annotations remain bounded public strings, never secret storage. Runtime capabilities come from image code, not deployment fields or a package selector.

The network requests egress because the standard mesh network is not an internal Docker network. To deny egress, an administrator must provision an internal network accepted by executor policy. Image, network, identity pool, resource, and approval policy still apply after schema validation.

## Checks

The repository's unified verification should include:

```sh
go test ./examples/hello ./pkg/mesh
```

Hello checks cover strict input handling, Unicode greeting output, schema compilation, signed SDK invocation, and unsigned-call rejection. This document does not claim those checks have been run.
