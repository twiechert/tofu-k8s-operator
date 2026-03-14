# TTL Auto-Deletion

Automatically delete a TofuProject after a configured duration. Useful for temporary environments, ephemeral test infrastructure, or time-boxed resources.

```yaml
spec:
  ttl: "24h"
```

## Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `ttl` | string | `""` (no TTL) | How long the project lives after creation. Uses Go duration format (e.g. `"24h"`, `"168h"`, `"72h"`) |

## How It Works

1. When `spec.ttl` is set, the operator calculates the expiry time from the project's creation timestamp.
2. `status.expiresAt` is set so you can see when the project will be deleted.
3. Once the current time passes the expiry, the operator issues a standard delete — triggering the finalizer's `tofu destroy` before removal.
4. If the TTL is removed or cleared, `status.expiresAt` is also cleared and the project lives indefinitely.

## Status

```bash
# Check when a project expires
kubectl get tofuproject my-project -o jsonpath='{.status.expiresAt}'
```

## Example — Ephemeral Preview Environment

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: preview-pr-42
spec:
  programRef:
    name: preview-env
  params:
    branch: "pr-42"
  ttl: "72h"
  autoApprove: true
```

This creates a preview environment that is automatically destroyed after 3 days.
