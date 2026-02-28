<p align="center">
  <img src="logo.png" alt="tofu-k8s-operator" width="200"/>
  <br/>
  <strong>OpenTofu Kubernetes Operator</strong>
  <br/>
  <em>A Kubernetes operator written in Go that lets you run OpenTofu declaratively using Custom Resources.</em>
</p>

## Features

- Declarative OpenTofu execution via CRDs
- Kubernetes backend for state (Secrets)
- Reusable programs (`TofuProgram`) — inline HCL or [git sources](doc/git-sources.md)
- Parameterized runs (`TofuProject`)
- Drift-safe: new Job created when inputs change
- [Cross-project output dependencies](doc/dependencies.md)
- [Provider plugin cache](doc/provider-cache.md) via PVC
- [Plan-then-approve](doc/plan-approve.md) workflow
- [kubectl plugin](doc/kubectl-plugin.md) — `kubectl tofu plan|approve|suspend|resume`
- Suspend mode — pause reconciliation entirely
- Sync interval — periodic re-reconciliation
- Automatic destroy via finalizer
- Leader election for HA deployments
- Job locking — one Job per project at a time

## Quick Start

### Deploy

Helm (recommended):
```bash
helm install tofu-k8s-operator ./charts/tofu-operator \
  --namespace tofu-system \
  --create-namespace
```

Raw manifests:
```bash
kubectl apply -k deploy/
```

### CRDs

**TofuProgram** defines the infrastructure code — either inline HCL or a git repository source, plus provider requirements.

**TofuProject** binds a program to a specific environment — parameters, backend config, and execution settings (autoApprove, suspend, syncInterval, cache, dependencies).

### Usage

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: my-program
spec:
  programHCL: |
    variable "name" { type = string }
    resource "null_resource" "example" {
      triggers = { name = var.name }
    }
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: my-project
spec:
  programRef:
    name: my-program
  params:
    name: "hello"
  autoApprove: true
```

## Documentation

| Topic | Description |
|-------|-------------|
| [Git Sources](doc/git-sources.md) | Using git repositories instead of inline HCL, private repo auth |
| [Plan-Then-Approve](doc/plan-approve.md) | Review `tofu plan` output before applying |
| [Cross-Project Dependencies](doc/dependencies.md) | Consume outputs from upstream projects as input params |
| [Provider Plugin Cache](doc/provider-cache.md) | Cache providers via PVC to speed up `tofu init` |
| [kubectl Plugin](doc/kubectl-plugin.md) | CLI for plan, approve, suspend, resume |
| [Examples](doc/examples.md) | AWS S3 bucket example and more |

## Build & Test

```bash
just build          # compile the binary
just docker-build   # build the container image
just test           # run unit tests
just test-cover     # run with coverage
just e2e            # end-to-end tests (requires Docker)
```

PRs welcome.
