# Cross-Project Output Dependencies

A TofuProject can declare dependencies on other TofuProjects and consume their `tofu output` values as input parameters. When upstream outputs change, downstream projects automatically re-reconcile.

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: downstream
spec:
  programRef:
    name: app
  autoApprove: true
  dependencies:
    - projectRef:
        name: upstream        # name of the upstream TofuProject
        namespace: default    # optional, defaults to same namespace
      outputs:
        vpc_id: vpc_id        # upstream output name → downstream param name
        subnet_id: subnet     # can map to different param names
```

## How it works

1. The controller resolves each dependency by fetching the upstream TofuProject's `status.outputs`
2. Upstream output values are merged into the downstream project's effective params (overriding `spec.params` on conflict)
3. If any upstream is not `Succeeded` or is missing a required output, the downstream enters `WaitingDependency` phase and requeues after 15s
4. Changes to upstream outputs are detected via a cross-project watch, triggering immediate re-reconciliation of dependents

See [`examples/tofuproject-dependency.yaml`](../examples/tofuproject-dependency.yaml) for a complete example.
