# External Params

Import parameters from ConfigMaps and Secrets instead of inlining them in the TofuProject spec. Three mechanisms are available, with clear precedence ordering.

## Precedence (lowest to highest)

1. **`valuesFrom`** — ordered bulk import by name (lowest)
2. **`paramFrom`** — bulk import with full ObjectRef (namespace support)
3. **`paramBindings`** — individual key selection
4. **`params`** — inline values
5. **dependency outputs** — highest

## `valuesFrom`

Bulk-import all keys from ConfigMaps/Secrets by name (always in the project's namespace). Later entries override earlier ones.

```yaml
spec:
  valuesFrom:
    - configMapRef: global-defaults
    - configMapRef: env-overrides
    - secretRef: sensitive-values
```

| Field | Type | Description |
|-------|------|-------------|
| `configMapRef` | string | Name of a ConfigMap to import all keys from |
| `secretRef` | string | Name of a Secret to import all keys from |

## `paramFrom`

Bulk-import all keys from ConfigMaps/Secrets using full ObjectRef (supports cross-namespace).

```yaml
spec:
  paramFrom:
    - configMapRef:
        name: shared-config
        namespace: infra   # optional, defaults to project namespace
    - secretRef:
        name: db-creds
```

## `paramBindings`

Select individual keys from ConfigMaps/Secrets and map them to named params.

```yaml
spec:
  paramBindings:
    - name: db_host
      configMapKeyRef:
        name: app-config
        key: database_host
    - name: api_token
      secretKeyRef:
        name: api-keys
        key: token
```

## Watch Triggers

The operator watches all referenced ConfigMaps and Secrets. When a referenced resource changes, the project is automatically re-reconciled — no manual intervention needed. This applies to all three mechanisms (`valuesFrom`, `paramFrom`, `paramBindings`).

## Example — Layered Configuration

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: staging-vpc
spec:
  programRef:
    name: vpc-module
  valuesFrom:
    - configMapRef: global-defaults      # base values
    - configMapRef: staging-overrides    # environment layer
    - secretRef: staging-secrets         # sensitive values
  paramFrom:
    - configMapRef:
        name: shared-infra
        namespace: platform              # cross-namespace import
  paramBindings:
    - name: db_password
      secretKeyRef:
        name: db-creds
        key: password
  params:
    environment: staging                  # inline always wins
  autoApprove: true
```

In this example, precedence flows bottom-up: `params.environment` overrides anything from the ConfigMaps/Secrets above it.
