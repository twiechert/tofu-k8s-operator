# Retry Policy

Automatically retry failed tofu Jobs before marking the project as `Error`.

```yaml
spec:
  retryPolicy:
    maxRetries: 3
    delay: "30s"
```

## Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxRetries` | integer | required | Maximum number of retry attempts |
| `delay` | string | `"30s"` | Duration to wait between retries (e.g. `"30s"`, `"1m"`, `"5m"`) |

## Behaviour

When a tofu Job fails (apply or plan-approve apply):

1. If `retryCount < maxRetries`, the operator:
   - Increments `status.retryCount`
   - Sets `status.phase` to `"Retrying"`
   - Deletes the failed Job
   - Requeues reconciliation after the configured `delay`
   - On the next reconcile, a new Job is created with the same name
2. If `retryCount >= maxRetries`, the project transitions to `"Error"` as usual.

On a successful apply, `retryCount` is reset to `0`.

## Status

The current retry count is visible in `status.retryCount`:

```bash
kubectl get tofuproject my-project -o jsonpath='{.status.retryCount}'
```

## Use Cases

- Transient cloud API rate limits or throttling
- Temporary network connectivity issues
- Eventual consistency delays (e.g. waiting for IAM propagation)
- Race conditions with other controllers or CI/CD systems

## Example

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: flaky-infra
spec:
  programRef:
    name: aws-vpc
  autoApprove: true
  retryPolicy:
    maxRetries: 3
    delay: "1m"
```

This will retry up to 3 times with 1-minute intervals between attempts. If all 3 retries fail, the project enters `Error` phase.
