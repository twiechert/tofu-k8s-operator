# Plan-Then-Approve Workflow

When `autoApprove: false`, the operator runs `tofu plan` first and waits for explicit approval before applying.

1. Operator creates a plan Job and sets phase to `Planning`
2. Once the plan completes, the plan output and summary are stored in `.status.planOutput` / `.status.planSummary`, and phase becomes `WaitingApproval`
3. Review the plan:
   ```bash
   kubectl tofu plan <project>
   ```
4. Approve:
   ```bash
   kubectl tofu approve <project>
   ```
5. The operator creates an apply Job and proceeds to `Succeeded`

If the spec changes while waiting for approval, the stale plan is invalidated and a new plan is created automatically.
