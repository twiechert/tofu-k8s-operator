# Resource Limits

Set CPU and memory requests/limits on tofu Job containers using `spec.resources`.

```yaml
spec:
  resources:
    limits:
      cpu: "500m"
      memory: "256Mi"
    requests:
      cpu: "100m"
      memory: "128Mi"
```

## Format

Values use standard Kubernetes quantity notation:

| Resource | Examples |
|----------|----------|
| CPU | `"100m"` (100 millicores), `"1"` (1 core), `"2.5"` (2.5 cores) |
| Memory | `"128Mi"` (128 MiB), `"1Gi"` (1 GiB), `"512M"` (512 MB) |

Both `limits` and `requests` are optional. You can set one without the other.

## Behaviour

- Resource settings are applied to the `tofu` container in all Job types (apply, plan, destroy, drift detection).
- Invalid quantity strings (e.g. `"not-a-number"`) cause a reconciliation error — the Job is not created and the error is reported in the project status.
- When no `resources` are specified, no resource constraints are set (Kubernetes defaults apply).

## Use Cases

- Preventing runaway memory consumption in large `tofu plan` operations
- Ensuring fair resource sharing in multi-tenant clusters
- Meeting namespace-level `ResourceQuota` or `LimitRange` requirements
- Right-sizing Jobs for cost optimization

## Example

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: large-infra
spec:
  programRef:
    name: vpc-module
  autoApprove: true
  resources:
    limits:
      cpu: "2"
      memory: "1Gi"
    requests:
      cpu: "500m"
      memory: "512Mi"
```
