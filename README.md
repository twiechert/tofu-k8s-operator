<p align="center">
  <img src="logo.png" alt="tofu-k8s-operator" width="200"/>
  <br/>
  <strong>OpenTofu Kubernetes Operator</strong>
  <br/>
  <em>A Kubernetes operator written in Go that lets you run OpenTofu declaratively using Custom Resources.</em>
</p>

<p align="center">
  <a href="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/ci.yaml"><img src="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/twiechert/tofu-k8s-operator/releases/latest"><img src="https://img.shields.io/github/v/release/twiechert/tofu-k8s-operator" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/twiechert/tofu-k8s-operator"><img src="https://goreportcard.com/badge/github.com/twiechert/tofu-k8s-operator" alt="Go Report Card"></a>
  <a href="https://github.com/twiechert/tofu-k8s-operator/blob/main/LICENSE"><img src="https://img.shields.io/github/license/twiechert/tofu-k8s-operator" alt="License"></a>
</p>

## Features

- Declarative OpenTofu execution via CRDs
- Kubernetes backend for state (Secrets)
- Reusable programs (`TofuProgram`) — inline HCL or [git sources](doc/git-sources.md)
- Parameterized runs (`TofuProject`) — inline, `paramFrom` (bulk ConfigMap/Secret), and `paramBindings` (individual key refs)
- Drift-safe: new Job created when inputs change (including ConfigMap/Secret watches)
- [Cross-project output dependencies](doc/dependencies.md)
- [Provider plugin cache](doc/provider-cache.md) via PVC
- [Plan-then-approve](doc/plan-approve.md) workflow
- [Delete protection](doc/delete-protection.md) — prevent accidental infrastructure destruction
- Configurable service account — custom SA or annotations (IRSA/workload identity)
- [kubectl plugin](doc/kubectl-plugin.md) — `kubectl tofu plan|approve|delete|suspend|resume`
- Suspend mode — pause reconciliation entirely
- Sync interval — periodic re-reconciliation
- Automatic destroy via finalizer
- Leader election for HA deployments
- Job locking — one Job per project at a time
- Custom environment variables — inject env vars and envFrom into tofu Jobs
- Resource limits — set CPU/memory requests and limits on Job containers
- Retry policy — automatically retry failed Jobs with configurable delay
- Drift detection — periodic plan-only jobs to detect infrastructure drift
- Webhook notifications — send HTTP POST notifications on lifecycle events
- [Ignore providers & additional providers HCL](doc/ignore-providers.md) — strip source provider/backend blocks, inject custom provider config

## Quick Start

### Deploy

Helm (recommended):
```bash
helm install tofu-k8s-operator ./charts/tofu-k8s-operator \
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

Reference a git repository (recommended):

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: my-program
spec:
  source:
    url: https://github.com/example/infra.git
    ref: main
    path: modules/vpc
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

Or use inline HCL for simple programs:

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
```

## Documentation

| Topic | Description |
|-------|-------------|
| [Git Sources](doc/git-sources.md) | Using git repositories instead of inline HCL, private repo auth |
| [Plan-Then-Approve](doc/plan-approve.md) | Review `tofu plan` output before applying |
| [External Params](doc/external-params.md) | Import params from ConfigMaps/Secrets via `paramFrom` and `paramBindings` |
| [Cross-Project Dependencies](doc/dependencies.md) | Consume outputs from upstream projects as input params |
| [Provider Plugin Cache](doc/provider-cache.md) | Cache providers via PVC to speed up `tofu init` |
| [Delete Protection](doc/delete-protection.md) | Prevent accidental infrastructure destruction |
| [kubectl Plugin](doc/kubectl-plugin.md) | CLI for plan, approve, delete, suspend, resume |
| [Environment Variables](doc/env-vars.md) | Inject env vars and envFrom into tofu Jobs |
| [Resource Limits](doc/resource-limits.md) | Set CPU/memory requests and limits on Job containers |
| [Retry Policy](doc/retry-policy.md) | Automatically retry failed Jobs with configurable delay |
| [Drift Detection](doc/drift-detection.md) | Periodic plan-only jobs to detect infrastructure drift |
| [Webhook Notifications](doc/webhooks.md) | HTTP POST notifications on lifecycle events |
| [Ignore Providers](doc/ignore-providers.md) | Strip source provider/backend blocks, inject custom provider config |
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
