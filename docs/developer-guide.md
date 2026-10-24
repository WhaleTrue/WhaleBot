# Developing WhaleMesh capabilities

## One SDK, no node roles

A node is a provider, consumer, or both. `pkg/mesh` has no LLM, storage, runtime, executor, or web role enum. The universal executable selects ordinary packages with `NODE_PACKAGES`; a separate binary can use the same SDK without joining that package list. See [`examples/hello`](../examples/hello/README.md) for a runnable provider and [`schema-contracts.md`](architecture/schema-contracts.md) for wire contracts.

A provider package follows the repository composition contract:

```go
func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error)
```

Register handlers with `node.Handle(method, path, scopes, handler)`. Return capabilities without calling `UpdateCapabilities`; the universal executable aggregates capabilities from all selected packages. Declare exact handler scopes on every `Operation.Scopes`. A bundled capability's `Scopes` is the union, not permission to invoke every operation. The authorized browser backend checks operation scopes against user grants before making a node-signed call. An omitted operation scope is not a public-business-endpoint declaration.

Use `node.AddWorker` for work tied to the node context, before `Start`, rather than an unowned background goroutine. Persist business data below the provided data directory. Do not treat membership as a storage replication system. Expose ownership and sticky-provider requirements for mutable state.

## Go module

The public SDK import is `github.com/WhaleTrue/WhaleBot/pkg/mesh`. Product branding does not change the repository's module path. Until this rewrite merges into `dev`, select its development branch explicitly:

```sh
go mod init example.com/my-capability
go get github.com/WhaleTrue/WhaleBot/pkg/mesh@feat/whalemesh-architecture-rewrite
```

For an unpublished local checkout, use `go mod edit -replace=github.com/WhaleTrue/WhaleBot=/absolute/path/to/WhaleBot` before resolving the dependency. Replace only your consumer module; no core SDK change is required. Implement a standalone executable following `examples/hello/main.go`, then build it in an ordinary Docker image.

## Capability contract

Choose a stable capability ID and interface name. Use strict SemVer. Describe input/output with Draft 2020-12 JSON Schema and validate input again at the business boundary; a schema in a manifest is descriptive, not automatic HTTP payload enforcement. Reject unknown input fields where appropriate. Use structured errors with a meaningful HTTP status.

List every callable operation with method, path, exact scopes, and semantics. Mark read-only, idempotent, mutating, side-effecting, and streaming independently. A read-only operation cannot also be mutating or side-effecting. Do not claim idempotence for an operation that creates another side effect on every call.

Primary input/output schemas are mandatory and non-null. No-input uses `{"type":"null"}`. Mark credential-bearing capabilities or individual operations `transport_security: tls`; this is a generic interface trait, not a capability-name convention.

Tool exports name an existing declared method/path, provide a model-compatible input schema, and list all scopes required by that operation. Tool scopes cannot exceed the capability's scope union. `confirmation` describes an interaction requirement; a caller-controlled boolean never proves that a trusted user approved execution. Runtime policy must enforce actual confirmation grants.

## Startup and credentials

Load configuration with `mesh.LoadConfigFromEnv`, construct `mesh.New`, register handlers, publish the aggregated capability list, and call `Start`. Shut down with `Close` under a bounded context. The hello executable shows the complete sequence.

Every principal needs a private Ed25519 identity file and a public trust file. A trust grant separates `scopes` (calls the principal can make) from `provides` (capabilities the principal can advertise). Empty `provides` denies advertisement. Bootstrap provisions independent keys; a provider must not generate a shared fallback identity when credentials are absent. Keep private identities and bootstrap passwords out of manifests, logs, images, and source control.

HTTP signatures authenticate requests, not transport confidentiality. Configure TLS or an appropriately private network when handling credentials or sensitive data. Restrict public handlers to liveness and browser endpoints with their own user authentication. Do not make business APIs public for convenience.

## Consumer flow

Declare requirements explicitly. An agent's required LLM and session-store dependencies are business-package configuration, not SDK role logic. Optional skills, memory, events, or statistics dependencies must remain optional rather than blocking every agent when absent.

Resolve a `mesh.Requirement` through `Node.Resolve`. Require both the intended interface and an appropriate version range. Add namespace, locality, attributes, scopes, pinning, and sticky selection when the business contract needs them. `CompatibleInterface` checks interface and version identity; it does not prove that unrelated providers share mutable state.

Use `mesh.IdentityKey(identity)` for exact instance pinning and signed destination targets. It escapes namespace/principal/logical-name/instance components. Bare instance IDs are not selectors. `Node.Candidates` enumerates eligible providers without changing schedules or sticky bindings; after safe catalog reads, `Resolve` accepts an exact `providers` allowlist. Declare a stable requirement ID to preserve stateful affinity.

Invoke a returned `mesh.Binding` with `Node.Call` or `Node.Stream`. The SDK signs the target request. Always close stream bodies and honor context cancellation. Resolution can fail while discovery converges, and a resolved provider can fail before the call. Surface those failures; do not replace a missing real service with fake data or an implicit mock. Mock LLM providers require explicit configuration.

For user-facing calls, node principal and browser user are different identities. Preserve authenticated owner context in signed request data and verify delegation permissions. An unsigned owner header is never authority to read another user's sessions or manage another user's deployment.

## Execution is a capability

Do not import Docker Engine clients into ordinary providers. Resolve executor capabilities for lifecycle, command jobs, file operations, and artifact export. The executor alone holds the socket and manages only its own labeled resources.

Use the full Deployment desired-state contract. All resource limits are explicit; secrets are references; mounts are owned named volumes; image and network remain policy-controlled. A deployment document cannot request privileged mode or raw Engine options. A schema-valid request can still fail safely if its requested network, secret, identity, or approval cannot be honored. Consult the actual executor error rather than stripping the requested safety setting.

`Deployment.node` contains only logical name, public labels/annotations and requirements. Runtime identity, host and advertised transport come from the authorized executor and SDK; capabilities come from the image implementation. Add trusted identity-pool capacity explicitly rather than sending credentials or a self-claimed running manifest in the deployment request.

## Development checks and contract changes

Add a small runnable regression check for new nontrivial behavior. Cover invalid external input and denied authorization, not only successful calls. Root verification uses the single Go module; do not introduce nested modules for example providers. Changes to a wire field require corresponding schema, Go type, decoder, consumer, example, and documentation changes. Unknown schema versions fail closed; there are no aliases for legacy orchestrator APIs.

The architecture decisions in [`architecture/adr/`](architecture/adr/) explain why these boundaries exist. The legacy schema audit records the code examined before the cutover. Test commands in documentation are instructions, not evidence of execution.
