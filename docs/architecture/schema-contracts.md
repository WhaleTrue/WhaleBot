# Schema contracts

The machine-readable source is `schemas/node.schema.json`, `schemas/capability.schema.json`, and `schemas/deployment.schema.json`. All use JSON Schema Draft 2020-12. `schemas.Files` embeds those exact resources. `pkg/mesh/schema.go` uses a real schema compiler and adds cross-field and execution-safety checks. It never downloads schema resources.

## Public validation API

- `ValidateManifest(Manifest) error`
- `ValidateCapability(Capability) error`
- `ValidateRequirement(Requirement) error`
- `ValidateDeployment(Deployment) error`
- `ValidateJSONSchema(json.RawMessage) error`
- `DecodeManifest([]byte) (Manifest, error)`
- `DecodeCapability([]byte) (Capability, error)`
- `DecodeDeployment([]byte) (Deployment, error)`
- `CompatibleVersion(offered, required string) bool`
- `CompatibleInterface(Capability, Requirement) bool`

Use a raw decoder at an external JSON boundary. Unmarshalling into a Go struct before validation loses unknown fields. The raw decoders reject unknown fields at every closed object, duplicate fields, trailing documents, documents above 1 MiB, and nesting above 64 levels. Struct validators cannot recover fields already discarded by a different decoder.

Objects reject undeclared fields. Bounded user maps are the deliberate exception. Go's nil slices and maps encode as `null`; collection schemas accept null where the SDK wire types permit nil. Missing optional collections mean no entries. Validation does not invent resource defaults or silently downgrade versions.

## Node: `whalemesh.node/v1`

Required top-level fields are `schema`, `identity`, `transport`, `provides`, `requires`, `lifecycle`, and `security`. Optional `metadata` is a bounded string map.

- `identity`: `namespace`, `name`, `instance_id`, `host_id`, `principal`, `labels`, `annotations`. Namespace and name are 1–63 lowercase name characters. Principal and instance ID are bounded identity tokens. SDK startup fills provisioned principal and creates a new instance ID.
- `transport`: `protocols` (`http`, `https`, `yamux`), `listen`, `advertise`, `reachability`, `security`. Advertised addresses must be absolute HTTP(S) URLs without user information, query, or fragment. Listen addresses are local binding descriptions, not remote service URLs.
- `provides`: at most 256 capability objects; IDs must be unique within the node.
- `requires`: at most 256 requirement objects. Nonempty requirement IDs must be unique.
- `lifecycle`: boolean `alive`, `ready`, `draining`.
- `security`: `identity`, `trust_namespace`, `authentication`, `permissions`. Identity and namespace must match the node identity. Declared permissions do not grant authority; the receiver's trust configuration does.

Metadata, labels, annotations, and capability/requirement attributes reject credential-like keys and recognizable credential material. URLs cannot smuggle credentials. This is a leak-prevention policy, not proof that arbitrary descriptive text contains no secret.

## Capability: `whalemesh.capability/v1`

Required fields are `schema`, `id`, `interface`, `version`, `endpoint`, `input_schema`, `output_schema`, `operations`, `ready`, `attributes`, `scopes`, and `semantics`. Optional fields are `transport_security` and `tool`. Endpoint is an absolute HTTP(S) URL or an absolute application path. Input/output schemas must be non-null object or boolean JSON Schemas that compile as Draft 2020-12 with only local references. Missing schemas are invalid; the SDK never fills an unrestricted schema for an incomplete provider.

An operation contains `name`, `method`, `path`, `semantics`, and optional wire fields `scopes` and `transport_security`. Providers declare every operation's exact handler scopes; the capability scopes are their union. The browser backend denies a business operation without declared scopes. Operation scopes must be a subset of capability scopes. Paths must be clean absolute paths without query, fragment, traversal, or control characters. Method/path pairs must be unique. Supported methods are GET, POST, PUT, PATCH, DELETE, HEAD, and OPTIONS. Capability or operation `transport_security: "tls"` requires encrypted transport; `"authenticated"` requires authenticated Mesh transport without asserting encryption.

Semantics require explicit boolean `read_only`, `idempotent`, `mutating`, `side_effecting`, and `streaming` fields. A read-only operation cannot also declare mutation or side effects. Idempotence does not imply read-only: an idempotent PUT may mutate state. Capability semantics summarize its operations; per-operation semantics remain authoritative for each HTTP route.

Primary schemas describe successful input/output values at the advertised endpoint, not a universal RPC envelope or every error response. A no-input operation uses `{"type":"null"}`. Mixed capabilities may publish a union for their actual HTTP shapes: Telegram accepts no input for reads or its configuration object for PUT, and returns public configuration or status without the token. LLM model/config reads accept no input; config PUT accepts the typed configuration document. Executor descriptors describe their primary endpoint operations; heterogeneous secondary HTTP operations retain their documented typed contracts, including the canonical deployment schema for deployment PUT. Browser HTML is a string with `contentMediaType: "text/html"`. Relay input is its `{instance_id,name}` hello; its null output denotes HTTP 101 with no JSON response body before the yamux stream. Streaming semantics do not invent a JSON response where the transport upgrades or carries SSE.

A tool export contains `name`, `description`, `input_schema`, `method`, `path`, `scopes`, and `confirmation`. Its method/path must reference a declared operation. Tool scopes must include the referenced operation's required scopes and be a subset of capability scopes. Tool metadata is not an authorization or confirmation grant.

## Requirements and version compatibility

A requirement has `id`, `capability`, `interface`, `version`, `namespace`, `locality`, `policy`, `pin`, `optional`, `many`, `sticky`, `attributes`, and `scopes`. At least capability or interface must be nonempty. An interface match is exact; a capability match does not substitute another interface name.

Versions are strict SemVer `major.minor.patch`, optionally with prerelease and build metadata. Leading zeros in numeric version/prerelease identifiers are invalid. Requirements support empty or `*`, exact versions, `=`, `>`, `>=`, `<`, `<=`, `^`, and `~`. Commas and spaces join comparisons with logical AND: `>=1.0.0 <2.0.0`. Operators must be attached to their version. OR, hyphen ranges, partial versions, and `v` prefixes are not supported. An invalid requirement is rejected, not treated as wildcard.

Caret preserves major compatibility; for major zero it preserves the first nonzero compatibility component. Tilde preserves major/minor. Prereleases require an explicit prerelease constraint on the same major/minor/patch release line. Build metadata does not change version ordering.

Locality values are empty/default, `any`, `node`, `host`, `local`, and `remote`. Policies are empty/default, `default`, `prefer-local`, `local-first`, `round-robin`, `random`, `least-load`, `pinned`, `sticky`, `broadcast`, `all`, `first`, and `failover`. Namespace, scopes, attributes, lifecycle, and local selection policy are additional resolver filters; `CompatibleInterface` only checks capability/interface/version compatibility.

## Deployment: `whalemesh.deployment/v1`

All desired-state fields are represented explicitly:

- `id`, `namespace`, `node`: executor-local deployment identity, trust namespace and a node template. The template requires `name` and optionally declares `labels`, `annotations`, and capability `requires`. It is not a node manifest: callers cannot choose identity, security, transports or provided capabilities. Executor policy supplies the pool principal, host, namespace, listeners, advertised routes and seeds.
- `image`, `command`: image reference and bounded argument vector. Command is not a shell fragment unless the image explicitly executes a shell.
- `env`: at most 128 bounded environment entries. Names follow environment syntax. Credential-like entries and `MESH_`/`NODE_` overrides are forbidden.
- `secrets`: at most 64 `{name,target}` references. Targets must be clean absolute paths below `/run/secrets/`. Secret bytes are never a deployment field.
- `volumes`: at most 32 `{source,target,read_only}` mounts. Sources are named volumes, not host paths. Targets cannot hide root, identity, `/proc`, `/sys`, `/dev`, `/etc`, or Docker socket paths, including reserved ancestors.
- `resources`: required `memory_bytes` (16 MiB–1 TiB), `nano_cpus` (1,000,000–64,000,000,000), and `pids_limit` (1–4096). These are outer wire bounds; executor policy may be narrower.
- `network`: `{name,egress,ports}`. Built-in `host`, `none`, and `bridge` network names are forbidden. An empty name lets executor policy select its owned network. Each port has `container_port`, `host_port`, and `protocol` (`tcp` or `udp`). Host port zero requests an allocated port. Duplicate container port/protocol pairs are rejected.
- `replicas`: 0–16.
- `scope`: `session`, `task`, or `global`. Session and task require their corresponding ID and a positive TTL. Global forbids session/task IDs.
- `session_id`, `task_id`, `ttl_seconds`: bounded ownership and lifetime. TTL is at most 604800 seconds (seven days); zero is permitted only for global scope.
- `restart_policy`: `no`, `on-failure`, or `unless-stopped`.
- `desired_state`: `running`, `stopped`, or `absent`.
- `labels`: bounded public strings; the `whalemesh.` prefix is executor-owned.
- `approval_ids`: at most 32 unique references to separately verified approvals.
- `requires`: optional capability requirements appended after optional `node.requires` into the child's SDK manifest. Each list and their combined result allow at most 256 requirements. Duplicate nonempty requirement IDs across both lists are rejected. Requested scopes must fit the assigned child's grant, not the executor's grant.

No schema field grants privileged mode, host mounts, host namespaces, added Linux capabilities, Docker devices, or arbitrary Engine options. Schema acceptance never bypasses executor ownership, image, network, resource, approval, or secret-resolution policy. The executor can reject a valid deployment if it cannot safely honor a requested field.

The node template's labels and annotations each allow at most 64 public entries, with 1–128-character keys and values up to 4096 characters. Credential-like metadata is rejected. The executor validates the assigned pool namespace and signing key before launch. Deployment input cannot select packages through `NODE_PACKAGES`.

## Evolution

Wire schema versions and capability interface versions serve different purposes. A new capability version does not change the node wire schema. Unknown schema versions fail closed. Update schemas, Go types, validators, examples, and consumers together when changing a wire contract. No legacy orchestrator registration shape is accepted as an alias.
