# Container Image Config Metadata

## Goal

OpenDeploy should support image-provided deployment metadata. A deployment image should be able to declare the typed configuration it requires. OpenDeploy can use that declaration to validate deployments and render a configuration UI.

This supports an IDE-like deployment workflow. The operator can specify an image and OpenDeploy can discover the expected environment variables, secrets, ports, defaults, and validation rules before the deployment is started.

## Metadata carriers

### Image labels

Docker and OCI images support labels on the image config. Labels are string key/value pairs set during image build.

```dockerfile
LABEL org.opencontainers.image.source="https://github.com/acme/app"
LABEL dev.opendeploy.config.schema='{"type":"object","properties":{"PORT":{"type":"integer"}}}'
```

Labels are simple and widely supported. OpenDeploy can read them by resolving the image manifest and image config blob. It does not need to pull the image layers.

Labels are a poor fit for large documents. They are flat strings, so they are best used for small pointers, digests, versions, or small inline schemas.

### OCI annotations

OCI manifests, image indexes, and descriptors support annotations. Common annotation keys include `org.opencontainers.image.title`, `org.opencontainers.image.description`, `org.opencontainers.image.version`, `org.opencontainers.image.source`, `org.opencontainers.image.revision`, `org.opencontainers.image.licenses`, and `org.opencontainers.image.documentation`.

Annotations are useful for descriptive image metadata. They are still string key/value pairs, and registry/client support is less uniform than image labels.

### Files inside the image

An image can include a schema file at a known path.

```text
/opendeploy/config.schema.json
/usr/share/opendeploy/config.schema.json
```

Files support rich metadata without encoding it into string labels. They are also easy for application authors to inspect in the source repository and in the built image.

The drawback is discovery. OpenDeploy usually needs to pull or extract image layers to read a file from the image filesystem. This is more expensive than reading labels or registry-side artifacts.

### OCI attached artifacts

OCI registries can attach related artifacts to an image digest through the OCI referrers model. Existing uses include SBOMs, signatures, provenance, and attestations.

OpenDeploy could attach a config schema artifact to an image digest with a custom media type.

```text
application/vnd.opendeploy.config.schema.v1+json
```

This is the cleanest model for rich image metadata. The schema is a separate OCI artifact, can be discovered by image digest, and does not require changing the image filesystem.

The tradeoff is registry compatibility. OCI referrer support is improving, but OpenDeploy should expect uneven support across registries.

### SBOMs, provenance, and attestations

Common image metadata ecosystems include SPDX SBOMs, CycloneDX SBOMs, SLSA provenance, in-toto attestations, and Sigstore/cosign signatures.

These are adjacent to deployment config metadata. They demonstrate the pattern of attaching structured metadata to an image digest, but they do not define the runtime configuration an application expects.

## Existing models

### OCI image annotations

OCI image annotations define common descriptive metadata. They do not define typed application configuration.

### Helm `values.schema.json`

Helm charts can include `values.schema.json` to validate chart values. This is a strong precedent for using JSON Schema to validate deployment input and drive form generation.

The model is chart-centric. It does not solve schema discovery for plain container images.

### Kubernetes CRD OpenAPI schemas

Kubernetes CRDs use OpenAPI schemas for typed resource validation. This is useful design precedent for validation, defaults, and UI generation.

The model applies to Kubernetes resources, not image-level deployment metadata.

### Docker Compose

The Docker Compose specification defines service-level deployment configuration such as environment variables, secrets, mounts, networks, health checks, and ports.

Compose does not provide a standard image-level schema that declares which configuration an image requires.

### CNAB and Porter

Cloud Native Application Bundle and Porter define bundles with typed parameters and credentials. They are closer to complete application packaging systems than image metadata systems.

They are useful prior art, but they are likely too heavyweight for OpenDeploy's image discovery path.

### Buildpacks

Buildpacks include metadata about build and runtime behavior. They do not define a general deployment configuration schema for arbitrary container images.

### Dev Container metadata

Dev Container metadata defines development environment configuration. It is adjacent to the IDE-like workflow, but it targets development containers rather than deployment images.

## Proposed OpenDeploy direction

OpenDeploy should define a schema format based on JSON Schema. JSON Schema already represents types, required fields, enums, defaults, descriptions, string formats, numeric bounds, arrays, and objects. OpenDeploy can add extension fields for deployment-specific semantics.

For source-built deployments, OpenDeploy should support a schema file in the repository next to `fleet.next`, for example `opendeploy.schema.json`.

When OpenDeploy builds an image, it should attach the schema to the produced image digest as an OCI artifact when the target registry supports it. It can also add a small label that points to the schema artifact or to a schema file inside the image.

For externally supplied images, OpenDeploy should discover schemas in this order:

1. Look for an OCI referrer with media type `application/vnd.opendeploy.config.schema.v1+json`.
2. Look for OpenDeploy image labels that point to a schema artifact, schema digest, schema URL, schema path, or small inline schema.
3. Optionally inspect a known file path inside the image as a fallback.

This gives OpenDeploy a registry-native path for rich metadata while preserving a simple fallback for images that only support labels or embedded files.

## Example schema

```json
{
  "$schema": "https://opendeploy.dev/schemas/deployment-config.v1.json",
  "type": "object",
  "required": ["DATABASE_URL", "PORT"],
  "properties": {
    "DATABASE_URL": {
      "type": "string",
      "format": "uri",
      "description": "Postgres connection string",
      "x-opendeploy-secret": true
    },
    "PORT": {
      "type": "integer",
      "default": 8080,
      "minimum": 1,
      "maximum": 65535
    },
    "LOG_LEVEL": {
      "type": "string",
      "enum": ["debug", "info", "warn", "error"],
      "default": "info"
    }
  }
}
```

## Open questions

- Decide whether OpenDeploy schemas should model environment variables directly or model typed application settings that later map to environment variables, files, command arguments, or secrets.
- Define secret handling semantics, including whether secret values are always references and never literal values.
- Define schema versioning and compatibility rules.
- Define how OpenDeploy handles missing schemas, unsupported registries, conflicting schemas, and schemas attached to mutable tags.
- Decide whether schema discovery should resolve images to immutable digests before reading metadata.
- Decide whether build-time repository schemas and image-attached schemas must be byte-identical or can diverge.
