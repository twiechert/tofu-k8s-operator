# Scheduled Apply Windows

Control **when** approved plans are applied by defining a cron-based maintenance window.

## Configuration

Add `applySchedule` to your TofuProject spec:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production-vpc
spec:
  programRef:
    name: aws-vpc
  autoApprove: false
  autoApproveMaxBlastRadius: 5
  applySchedule:
    schedule: "0 2 * * *"   # daily at 2:00 AM UTC
    window: "1h"             # window stays open for 1 hour
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `schedule` | yes | — | Standard 5-field cron expression (minute, hour, day-of-month, month, day-of-week) |
| `window` | no | `1h` | Go duration string for how long the window stays open after the cron fires |

## Behavior

| Scenario | Result |
|----------|--------|
| `autoApprove: true` | Schedule is **ignored** — applies run immediately |
| Plan approved (manual or blast radius), **inside** window | Apply proceeds immediately |
| Plan approved, **outside** window | Phase set to `ScheduledApply`, requeued at next window open |
| No `applySchedule` configured | Apply proceeds immediately after approval (existing behavior) |

## How it works

1. A plan completes and enters `WaitingApproval`
2. The plan is approved (manually via `kubectl tofu approve` or automatically via blast radius threshold)
3. Before creating the apply Job, the controller checks `isWithinApplyWindow()`
4. If inside the window: apply proceeds normally
5. If outside the window: phase is set to `ScheduledApply` with a message indicating the next window time, and the reconciler requeues at the next window start

## Examples

Daily 2 AM window (1 hour):
```yaml
applySchedule:
  schedule: "0 2 * * *"
  window: "1h"
```

Weekday business hours (8 AM - 6 PM):
```yaml
applySchedule:
  schedule: "0 8 * * 1-5"
  window: "10h"
```

Every Saturday at midnight (4 hour window):
```yaml
applySchedule:
  schedule: "0 0 * * 6"
  window: "4h"
```

## Interaction with blast radius auto-approve

When both `autoApproveMaxBlastRadius` and `applySchedule` are configured:

1. Plan completes, blast radius is within threshold
2. Controller sets the approval annotation automatically
3. Controller checks the apply window
4. If outside the window, phase becomes `ScheduledApply` with message: `"Plan auto-approved (blast radius N <= threshold M), waiting for apply window (next: <time>)"`
5. At the next window open, the controller requeues and the apply proceeds
