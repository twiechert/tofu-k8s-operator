# Environment Variables

Inject custom environment variables into tofu Job containers using `spec.env` and `spec.envFrom`.

## `spec.env` — Individual Variables

```yaml
spec:
  env:
    - name: AWS_REGION
      value: us-east-1
    - name: TF_LOG
      value: DEBUG
```

Supports all standard Kubernetes env var sources:

```yaml
spec:
  env:
    - name: DB_PASSWORD
      valueFrom:
        secretKeyRef:
          name: db-credentials
          key: password
    - name: APP_CONFIG
      valueFrom:
        configMapKeyRef:
          name: app-settings
          key: config-value
    - name: POD_NAME
      valueFrom:
        fieldRef:
          fieldPath: metadata.name
```

## `spec.envFrom` — Bulk Import

Import all keys from a ConfigMap or Secret as environment variables:

```yaml
spec:
  envFrom:
    - configMapRef:
        name: shared-env
    - secretRef:
        name: cloud-credentials
      prefix: CLOUD_
```

The optional `prefix` prepends a string to each imported key (e.g. a key `token` becomes `CLOUD_token`).

## Precedence

Environment variables are appended to the container's env list in this order:

1. Operator-internal env vars (e.g. `TF_PLUGIN_CACHE_DIR` from cache config)
2. `spec.env` values
3. `spec.envFrom` values

If the same variable name appears in multiple sources, Kubernetes uses the last value in the list.

## Use Cases

- Passing cloud provider credentials (`AWS_ACCESS_KEY_ID`, `GOOGLE_CREDENTIALS`, etc.)
- Controlling OpenTofu log verbosity (`TF_LOG`, `TF_LOG_CORE`)
- Setting HTTP proxy configuration (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`)
- Injecting secrets from external secret managers

## Example

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production
spec:
  programRef:
    name: infra
  autoApprove: true
  env:
    - name: TF_LOG
      value: WARN
    - name: AWS_DEFAULT_REGION
      value: eu-west-1
  envFrom:
    - secretRef:
        name: aws-credentials
```
