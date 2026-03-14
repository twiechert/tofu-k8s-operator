# Delete Protection

When `deleteProtection: true` is set on a TofuProject, the operator blocks `tofu destroy` until the deletion is explicitly approved. This prevents accidental infrastructure destruction.

## How It Works

1. Set `deleteProtection: true` in the TofuProject spec:
   ```yaml
   apiVersion: tofu.example.com/v1alpha1
   kind: TofuProject
   metadata:
     name: my-project
   spec:
     programRef:
       name: my-program
     deleteProtection: true
   ```

2. When you delete the TofuProject (`kubectl delete tofuproject my-project`), the operator sets the phase to `WaitingDeleteApproval` instead of running `tofu destroy`.

3. Approve the deletion:
   ```bash
   kubectl tofu delete my-project
   ```

4. The operator proceeds with `tofu destroy` and removes the finalizer.

## Annotation

Under the hood, `kubectl tofu delete` patches the annotation `tofu.example.com/approved-delete: "true"` on the project. The annotation change triggers a reconcile, which sees the approval and proceeds with the destroy flow.
