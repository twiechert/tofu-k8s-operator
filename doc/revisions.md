# Revision History & Pinned Revisions

## Overview

The operator stores an immutable audit trail of every apply (successful or failed) as a **revision ConfigMap**. Each revision captures the apply status, the full plan/apply output (so you can see what changed), the snapshot of what was applied, and the outputs produced.

A **pinned revision** lets you roll back to any stored revision by setting `spec.pinnedRevision` in the TofuProject CR. This is GitOps-compatible: the pin is declared in git.

## Revision ConfigMaps

On each apply (success or failure), the operator creates a ConfigMap named `{project}-rev-{number}`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-rev-3
  labels:
    app.kubernetes.io/managed-by: tofu-k8s-operator
    tofu.example.com/project: demo
    tofu.example.com/revision: "3"
    tofu.example.com/resource-type: revision
data:
  revision: "3"
  status: "succeeded"            # "succeeded" or "failed"
  appliedHash: "a1b2c3d4..."
  jobName: "demo-apply-a1b2c3d4"
  timestamp: "2026-03-01T12:00:00Z"
  planSummary: "Plan: 2 to add, 0 to change, 0 to destroy."
  planOutput: |                  # full plan/apply output (effective changes)
    Terraform will perform the following actions:
      # null_resource.example will be created
      ...
    Plan: 2 to add, 0 to change, 0 to destroy.
  outputs: '{"pet_name":"tofu-cat"}'
  snapshot:backend.tf: |         # TF file snapshot (succeeded only)
    terraform { backend "kubernetes" { ... } }
  snapshot:terraform.tfvars.json: |
    {"name": "hello"}
  snapshot:main.tf: |
    resource "null_resource" "example" { ... }
```

Key details:
- **`status`** — `"succeeded"` or `"failed"`, so you can see at a glance what happened.
- **`planOutput`** — the full plan/apply output showing effective changes. For plan-approve flow, this is the plan output from the planning phase. For auto-approve flow, this is the apply job logs (which include the plan).
- **`snapshot:*`** — TF file snapshots are only stored for successful applies. Failed revisions skip the snapshot since the files may be stale.
- Revision ConfigMaps are **not owned** by the TofuProject, so they survive project deletion for audit purposes.
- Failed applies only create a revision after all retries are exhausted.

## Configuration

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: demo
spec:
  programRef:
    name: my-program
  autoApprove: true
  revisionHistoryLimit: 10   # default 10, 0 = keep all
  pinnedRevision: 0          # 0 = normal flow, >0 = use stored revision
```

| Field | Default | Description |
|-------|---------|-------------|
| `revisionHistoryLimit` | `10` | Maximum number of revision ConfigMaps to retain. Set to `0` to keep all. |
| `pinnedRevision` | `0` | Pin to a stored revision number for rollback. `0` = normal flow. |

The current revision number is tracked in `status.revision`.

## Listing Revisions

```bash
kubectl tofu history demo -n default
```

```
REVISION   STATUS       HASH         AGE    SUMMARY
1          succeeded    a1b2c3d4     5d     Plan: 2 to add, 0 to change, 0 to destroy.
2          failed       e5f6g7h8     3d     Plan: 0 to add, 1 to change, 0 to destroy.
3          succeeded    i9j0k1l2     1h     Plan: 0 to add, 1 to change, 0 to destroy.
```

Or query directly:

```bash
kubectl get cm -l tofu.example.com/project=demo,tofu.example.com/resource-type=revision
```

## Viewing Revision Details

Show full details of a specific revision, including the plan/apply output:

```bash
kubectl tofu show demo 3 -n default
```

```
Revision:    3
Status:      succeeded
Hash:        i9j0k1l2...
Job:         demo-apply-i9j0k1l2
Timestamp:   2026-03-01T12:00:00Z
Summary:     Plan: 0 to add, 1 to change, 0 to destroy.
Outputs:     {"pet_name":"tofu-cat"}

--- Plan/Apply Output ---
Terraform will perform the following actions:
  # null_resource.example will be updated in-place
  ...
Plan: 0 to add, 1 to change, 0 to destroy.
```

## Pinning a Revision (Rollback)

Pin to a stored revision:

```bash
kubectl tofu pin demo 1 -n default
```

This patches `spec.pinnedRevision: 1`. The controller will:

1. Read the snapshot from `demo-rev-1`
2. Overwrite the project's `-tf` ConfigMap with the snapshot data
3. Run `tofu apply -auto-approve` using the snapshot (inline mode, no git clone)
4. Set `status.phase: Succeeded` on success

Pinned revisions always use **auto-approve** since the user explicitly chose the revision, and always run in **inline mode** since the snapshot contains all rendered files. This means rollback works even when git is down.

Only **succeeded** revisions can be pinned (failed revisions have no snapshot data).

## Unpinning

Resume normal flow:

```bash
kubectl tofu unpin demo -n default
```

This patches `spec.pinnedRevision: 0` and the controller resumes computing from the current spec.

## Cleanup

The operator automatically cleans up old revisions beyond the configured limit. The cleanup **never deletes** the currently pinned revision. Revisions are deleted oldest-first.

## Error Handling

- Pinning a nonexistent revision sets `status.phase: Error` with a message like `Pinned revision 99 not found`.
- Pinning a failed revision (no snapshot) sets `status.phase: Error`.
- If the pinned apply job fails, `status.phase: Error` is set.
- Revision creation and cleanup errors are non-fatal and logged.
