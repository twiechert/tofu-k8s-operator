# Webhook Notifications

Send HTTP POST notifications on lifecycle events to external services.

```yaml
spec:
  notifications:
    webhooks:
      - url: https://hooks.slack.com/services/T.../B.../xxx
        events: ["apply:success", "apply:error"]
      - url: https://my-api.example.com/tofu-events
        events: ["apply:success", "apply:error", "drift:detected", "plan:complete"]
```

## Events

| Event | Trigger |
|-------|---------|
| `apply:success` | Apply Job completed successfully |
| `apply:error` | Apply Job failed (after all retries exhausted, if retry policy is configured) |
| `drift:detected` | Drift detection found infrastructure changes |
| `plan:complete` | Plan Job completed, project is waiting for approval |

## Payload

Each webhook receives a JSON POST body:

```json
{
  "project": "my-project",
  "namespace": "default",
  "event": "apply:success",
  "phase": "Succeeded",
  "message": "",
  "timestamp": "2025-01-15T10:30:00Z"
}
```

| Field | Description |
|-------|-------------|
| `project` | Name of the TofuProject resource |
| `namespace` | Namespace of the TofuProject |
| `event` | The event that triggered the notification |
| `phase` | Current status phase at the time of the event |
| `message` | Status message (may be empty on success) |
| `timestamp` | RFC3339 UTC timestamp of when the event occurred |

## Behaviour

- Webhooks use a 10-second HTTP timeout.
- Failed webhook deliveries are logged but **do not block reconciliation**. The operator continues processing even if all webhooks fail.
- Each webhook's `events` list acts as a filter — only matching events trigger a POST to that URL.
- Multiple webhooks can subscribe to the same event.

## Use Cases

- Slack/Teams/Discord notifications for apply outcomes
- PagerDuty alerts on apply failures
- Audit logging to external systems
- Triggering downstream CI/CD pipelines after successful applies
- Drift alerting to security/compliance channels

## Example: Slack Integration

Using a Slack incoming webhook:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production
spec:
  programRef:
    name: infra
  autoApprove: true
  notifications:
    webhooks:
      - url: https://hooks.slack.com/services/T.../B.../xxx
        events:
          - apply:success
          - apply:error
          - drift:detected
```

Note: The payload format is a simple JSON object, not a native Slack message format. For Slack-formatted messages, route through an intermediary webhook processor (e.g. AWS Lambda, a small HTTP service) that transforms the payload.

## Example: Multiple Channels

Route different events to different endpoints:

```yaml
spec:
  notifications:
    webhooks:
      - url: https://my-api.example.com/audit
        events: ["apply:success", "apply:error", "plan:complete", "drift:detected"]
      - url: https://pagerduty.example.com/alert
        events: ["apply:error"]
      - url: https://slack.example.com/infra-channel
        events: ["drift:detected"]
```
