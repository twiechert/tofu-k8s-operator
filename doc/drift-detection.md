# Drift Detection

Periodically run `tofu plan` to detect infrastructure drift — changes made outside of the operator.

```yaml
spec:
  driftDetection:
    enabled: true
    interval: "15m"
```

## Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable or disable drift detection |
| `interval` | string | `"15m"` | How often to run drift checks (e.g. `"10m"`, `"1h"`, `"30m"`) |

## How It Works

1. After a successful apply (when `status.phase` is `"Succeeded"` and the spec hash hasn't changed), the operator checks if drift detection is enabled.
2. If enough time has elapsed since `status.lastDriftCheckTime`, a plan-only Job is created with the label `tofu.example.com/job-type=drift`.
3. When the plan Job completes:
   - **No changes detected**: `status.driftDetected` is set to `false`. The operator requeues after the configured interval.
   - **Changes detected**: `status.driftDetected` is set to `true`, `status.syncStatus` is set to `"not in sync"`, and a `drift:detected` notification is sent (if webhooks are configured).
4. The completed drift Job is automatically deleted to avoid accumulation.
5. During drift checking, `status.phase` is set to `"DriftChecking"` — this is treated as a healthy state (Ready=True) to avoid condition flapping.

## Auto-Remediation

When `spec.keepInSync` is `true` and drift is detected, the operator automatically clears `status.lastAppliedHash`, which triggers a re-apply on the next reconciliation cycle:

```yaml
spec:
  driftDetection:
    enabled: true
    interval: "30m"
  keepInSync: true
  autoApprove: true
```

Without `keepInSync`, drift is only reported — no automatic remediation occurs.

## Status

```bash
# Check if drift was detected
kubectl get tofuproject my-project -o jsonpath='{.status.driftDetected}'

# Check last drift check time
kubectl get tofuproject my-project -o jsonpath='{.status.lastDriftCheckTime}'

# Check sync status
kubectl get tofuproject my-project -o jsonpath='{.status.syncStatus}'
```

## Use Cases

- Detecting manual changes made via the cloud console
- Compliance auditing — ensuring infrastructure matches declared state
- Combined with webhooks for alerting on unexpected changes
- Auto-remediation of unauthorized modifications with `keepInSync: true`

## Example

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production-vpc
spec:
  programRef:
    name: vpc-module
  autoApprove: true
  keepInSync: true
  syncInterval: "5m"
  driftDetection:
    enabled: true
    interval: "1h"
  notifications:
    webhooks:
      - url: https://hooks.slack.com/services/T.../B.../xxx
        events: ["drift:detected"]
```

This checks for drift every hour. If drift is found, a Slack notification is sent and the infrastructure is automatically re-applied.
