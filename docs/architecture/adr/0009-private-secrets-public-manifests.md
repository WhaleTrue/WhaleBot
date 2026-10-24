# ADR 0009: Keep secret material outside public manifests

Status: Accepted

## Context

Discovery replicates manifests across peers. Placing a password, token, private key, or model credential in metadata turns discovery into a credential distribution channel.

## Decision

Public manifests contain identity names, permissions, interfaces, schemas, and bounded descriptive metadata, not credentials. Go validators reject credential-like metadata keys, obvious secret material, and endpoint URLs with credentials or query strings. Capability input schemas may describe a secret field; that schema is not a secret value.

Deployment secrets are named references with absolute targets under `/run/secrets/`. Environment variables are not a path for embedding secret bytes in a deployment. Private node credentials remain in identity volumes. The authorized secrets provider owns secret storage and resolution; discovery does not resolve references or receive secret values.

## Consequences

Secret resolution requires an explicit invocation grant and private transport where confidentiality matters. Validation catches recognizable leaks but cannot prove an arbitrary string is not a secret; producers remain responsible for avoiding secret values. Logs and model outputs need their own redaction at business boundaries. Copying a manifest never supplies the private key needed to act as its principal.
