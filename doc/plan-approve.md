# Plan-Then-Approve Workflow

When `autoApprove: false`, the operator runs `tofu plan` first and waits for explicit approval before applying.

## Annotation-Based Approval (Default)

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

## GitHub PR-Based Approval

Instead of using kubectl annotations, you can configure the operator to create a GitHub Pull Request for each plan. Merging the PR triggers the apply.

### Configuration

```yaml
spec:
  autoApprove: false
  approval:
    mode: githubPR
    github:
      tokenSecretRef:
        name: gh-token       # Secret containing a GitHub token with repo scope
        key: token
      repo: "org/infra-plans" # GitHub repo where PRs are created
      commitDiff: true         # commit plan output as a file in the PR
      diffPath: "plans/"       # directory for committed plan files (default: "plans/")
```

### Prerequisites

1. Create a GitHub token with `repo` scope and store it in a Kubernetes Secret:
   ```bash
   kubectl create secret generic gh-token --from-literal=token=ghp_xxxxxxxxxxxx
   ```
2. The target repository (`repo`) must exist. The operator will create branches and PRs in it.

### Flow

1. Operator creates a plan Job and sets phase to `Planning`
2. Plan completes — operator creates a GitHub PR in the configured repo:
   - PR body contains the full plan output, summary, and blast radius
   - If `commitDiff: true`, the plan output is also committed as a file at `{diffPath}/{namespace}/{project-name}/{hash}.txt`
3. Phase becomes `WaitingApproval` with the PR URL in `.status.pendingPRURL`
4. **Merge the PR** to approve the plan
5. The operator detects the merge (polls every 30s), sets the approval annotation, and creates the apply Job
6. After successful apply, the PR branch is cleaned up

### Rejection

If the PR is **closed without merging**, the plan is rejected:

- Phase transitions to `PlanRejected`
- Plan state is cleared (pendingPlanHash, planOutput, etc.)
- The PR branch is deleted
- On the next reconcile, a new plan will be created if the spec hash hasn't been applied yet

### Stale Plans

If the spec changes while a PR is open:
- The existing PR is closed with a comment explaining the spec changed
- The stale plan is invalidated
- A new plan Job is created, resulting in a new PR

### Status Fields

| Field | Description |
|-------|-------------|
| `status.pendingPRNumber` | GitHub PR number awaiting merge |
| `status.pendingPRURL` | URL of the pending GitHub PR |

### Example

See [`examples/tofuproject-github-pr.yaml`](../examples/tofuproject-github-pr.yaml).
