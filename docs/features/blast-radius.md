# Blast Radius Tracking & Conditional Auto-Approve

The operator parses `tofu plan` output into structured **blast radius** counts and exposes them in the project status. Combined with the `autoApproveMaxBlastRadius` spec field, low-risk plans can be auto-approved while high-impact changes still require human review.

## Status Fields

After every plan (including drift detection), `status.blastRadius` is populated:

```yaml
status:
  blastRadius:
    add: 2
    change: 0
    destroy: 1
    total: 3
  planSummary: "Plan: 2 to add, 0 to change, 1 to destroy."
```

The `total` field is `add + change + destroy`.

## Auto-Approve by Blast Radius

Set `autoApproveMaxBlastRadius` on a project to auto-approve plans within a threshold:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: staging-vpc
spec:
  programRef:
    name: aws-vpc
  autoApprove: false
  autoApproveMaxBlastRadius: 5
```

### Behavior

| `autoApprove` | `autoApproveMaxBlastRadius` | Plan Result | Action |
|---|---|---|---|
| `true` | (ignored) | any | Applied immediately (existing behavior) |
| `false` | unset / `nil` | any | Manual approval required (existing behavior) |
| `false` | `0` | No changes | Auto-approved |
| `false` | `0` | 1+ resources | Manual approval required |
| `false` | `5` | total <= 5 | Auto-approved |
| `false` | `5` | total > 5 | Manual approval required |

When auto-approved, the status message indicates:
```
Plan auto-approved (blast radius 3 <= threshold 5)
```

A `plan:auto-approved` notification is sent via webhooks.

### How It Works

1. Plan job completes
2. Operator extracts the plan summary line (e.g. `"Plan: 2 to add, 0 to change, 1 to destroy."`)
3. Parses into `{add: 2, change: 0, destroy: 1, total: 3}`
4. Stores in `status.blastRadius`
5. If `autoApproveMaxBlastRadius` is set and `total <= threshold`:
   - Sets the `tofu.example.com/approved-hash` annotation
   - Requeues the reconciliation
   - On next reconcile, the existing approval flow picks up the annotation and creates the apply job

This reuses the entire existing approval flow rather than duplicating apply-job creation logic.

## kubectl Plugin

`kubectl tofu plan` displays the blast radius when available:

```
$ kubectl tofu plan staging-vpc
Project:  default/staging-vpc
Phase:    WaitingApproval
Plan Hash: a1b2c3d4
Summary:  Plan: 2 to add, 0 to change, 1 to destroy.
Blast Radius: 2 to add, 0 to change, 1 to destroy (total: 3)

--- Plan Output ---
...
```

## Drift Detection

Blast radius is also populated during drift detection, making drift severity visible in the status even before any apply.
