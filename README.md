<p align="center">
  <img src="logo.png" alt="tofu-k8s-operator" width="200"/>
  <br/>
  <strong>OpenTofu Kubernetes Operator</strong>
  <br/>
  <em>A Kubernetes operator that runs OpenTofu/Terraform declaratively via CRDs — plan/approve workflows, drift detection, rollback, and GitOps-native infrastructure management.</em>
</p>

<p align="center">
  <a href="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/ci.yaml"><img src="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/e2e.yaml"><img src="https://github.com/twiechert/tofu-k8s-operator/actions/workflows/e2e.yaml/badge.svg" alt="E2E"></a>
  <a href="https://github.com/twiechert/tofu-k8s-operator/releases/latest"><img src="https://img.shields.io/github/v/release/twiechert/tofu-k8s-operator" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/twiechert/tofu-k8s-operator"><img src="https://goreportcard.com/badge/github.com/twiechert/tofu-k8s-operator" alt="Go Report Card"></a>
  <a href="https://github.com/twiechert/tofu-k8s-operator/blob/main/LICENSE"><img src="https://img.shields.io/github/license/twiechert/tofu-k8s-operator" alt="License"></a>
</p>

Manage cloud infrastructure directly from Kubernetes. Define your OpenTofu/Terraform code and variables as Custom Resources, and the operator handles planning, approval, applying, drift detection, and rollback — fully GitOps-compatible with ArgoCD and Flux.

## How It Works

```
TofuProgram (HCL or git repo)  +  TofuProject (params, settings)
        │                                │
        └──────────┬─────────────────────┘
                   ▼
         Operator renders generated files (backend, vars, providers) into a ConfigMap
         Git sources are cloned at runtime via init container — not stored in the ConfigMap
                   ▼
         Kubernetes Job runs `tofu plan` / `tofu apply`
                   ▼
         State stored in Kubernetes Secrets, outputs written to status
```

1. **`TofuProgram`** — define your infrastructure code (inline HCL or a git repo) and provider requirements
2. **`TofuProject`** — bind a program to an environment with parameters, approval settings, and scheduling
3. **The operator** — renders config, runs Jobs, tracks state, detects drift, and stores revision history

## Features

- Declarative OpenTofu execution via CRDs
- Kubernetes backend for state (Secrets)
- Reusable programs (`TofuProgram`) — inline HCL or [git sources](doc/git-sources.md)
- Parameterized runs (`TofuProject`) — inline, `valuesFrom` (ordered layering), `paramFrom` (bulk ConfigMap/Secret), and `paramBindings` (individual key refs)
- Drift-safe: new Job created when inputs change (including ConfigMap/Secret watches)
- [Cross-project output dependencies](doc/dependencies.md)
- [Provider plugin cache](doc/provider-cache.md) via PVC
- [Plan-then-approve](doc/plan-approve.md) workflow — via kubectl annotation or [GitHub PR approval](doc/plan-approve.md#github-pr-based-approval)
- [Delete protection](doc/delete-protection.md) — prevent accidental infrastructure destruction
- Configurable service account — custom SA or annotations (IRSA/workload identity)
- [kubectl plugin](doc/kubectl-plugin.md) — `kubectl tofu plan|approve|delete|suspend|resume|diff`
- Suspend mode — pause reconciliation entirely
- Sync interval — periodic re-reconciliation
- Automatic destroy via finalizer
- Leader election for HA deployments
- Job locking — one Job per project at a time
- Custom environment variables — inject env vars and envFrom into tofu Jobs
- [Extra volumes](doc/extra-volumes.md) — mount ConfigMaps, Secrets, PVCs, or image volumes into tofu Jobs
- Resource limits — set CPU/memory requests and limits on Job containers
- Retry policy — automatically retry failed Jobs with configurable delay
- Drift detection — periodic plan-only jobs to detect infrastructure drift
- Webhook notifications — send HTTP POST notifications on lifecycle events
- [Validation chain](doc/validation.md) — `tofu validate` + standard tools (tflint, checkov, trivy) or custom commands as init containers
- [Ignore providers & additional providers HCL](doc/ignore-providers.md) — strip source provider/backend blocks, inject custom provider config
- [Blast radius tracking](doc/blast-radius.md) — parsed plan counts with conditional auto-approve by threshold
- [Revision history & pinned revisions](doc/revisions.md) — audit trail of every apply, rollback to any stored revision
- [Scheduled apply windows](doc/scheduled-apply.md) — gate when approved plans are applied using cron-based maintenance windows
- [Revision diff](doc/kubectl-plugin.md) — compare two stored revisions to see what changed between applies
- [TTL auto-deletion](doc/ttl.md) — automatically delete projects after a configured duration

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

### kubectl Plugin

Install the `kubectl tofu` plugin for plan inspection, approval, suspend/resume, and revision management:

```bash
# Build and copy to PATH
just build-plugin
cp bin/kubectl-tofu /usr/local/bin/

# Or install directly via go install
just install-plugin
```

Verify it works:
```bash
kubectl tofu --help
```

See [kubectl Plugin](doc/kubectl-plugin.md) for the full command reference.

### CRDs

**TofuProgram** defines the infrastructure code — either inline HCL or a git repository source, plus provider requirements.

**TofuProject** binds a program to a specific environment — parameters, backend config, and execution settings (autoApprove, suspend, syncInterval, cache, dependencies).

### Usage

Reference a git repository (recommended):

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: aws-vpc
spec:
  source:
    url: https://github.com/acme-corp/infrastructure.git
    ref: main
    path: modules/vpc
  providers:
    - name: aws
      source: "hashicorp/aws"
      version: "~> 5.0"
      configHCL: |
        region = var.aws_region
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: staging-vpc
spec:
  programRef:
    name: aws-vpc
  params:
    aws_region: "eu-central-1"
    vpc_cidr: "10.1.0.0/16"
    environment: "staging"
  autoApprove: false
  syncInterval: "1h"
```

Or use inline HCL for simpler resources:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: s3-bucket
spec:
  providers:
    - name: aws
      source: "hashicorp/aws"
      version: "~> 5.0"
      configHCL: |
        region = var.aws_region
  programHCL: |
    variable "aws_region" { type = string }
    variable "bucket_name" { type = string }
    variable "environment" { type = string }

    resource "aws_s3_bucket" "this" {
      bucket = var.bucket_name
      tags = {
        Environment = var.environment
        ManagedBy   = "tofu-k8s-operator"
      }
    }

    resource "aws_s3_bucket_versioning" "this" {
      bucket = aws_s3_bucket.this.id
      versioning_configuration {
        status = "Enabled"
      }
    }

    output "bucket_arn" {
      value = aws_s3_bucket.this.arn
    }
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: logs-bucket
spec:
  programRef:
    name: s3-bucket
  params:
    aws_region: "eu-central-1"
    bucket_name: "acme-staging-logs"
    environment: "staging"
  autoApprove: true
```

## Documentation

| Topic | Description |
|-------|-------------|
| [Git Sources](doc/git-sources.md) | Using git repositories instead of inline HCL, private repo auth |
| [Plan-Then-Approve](doc/plan-approve.md) | Review `tofu plan` output before applying — annotation or GitHub PR approval |
| [External Params](doc/external-params.md) | Import params from ConfigMaps/Secrets via `valuesFrom`, `paramFrom`, and `paramBindings` |
| [Cross-Project Dependencies](doc/dependencies.md) | Consume outputs from upstream projects as input params |
| [Provider Plugin Cache](doc/provider-cache.md) | Cache providers via PVC to speed up `tofu init` |
| [Delete Protection](doc/delete-protection.md) | Prevent accidental infrastructure destruction |
| [kubectl Plugin](doc/kubectl-plugin.md) | CLI for plan, approve, logs, delete, suspend, resume, diff |
| [Environment Variables](doc/env-vars.md) | Inject env vars and envFrom into tofu Jobs |
| [Extra Volumes](doc/extra-volumes.md) | Mount ConfigMaps, Secrets, PVCs, or image volumes into tofu Jobs |
| [Resource Limits](doc/resource-limits.md) | Set CPU/memory requests and limits on Job containers |
| [Retry Policy](doc/retry-policy.md) | Automatically retry failed Jobs with configurable delay |
| [Drift Detection](doc/drift-detection.md) | Periodic plan-only jobs to detect infrastructure drift |
| [Webhook Notifications](doc/webhooks.md) | HTTP POST notifications on lifecycle events |
| [Validation](doc/validation.md) | Pre-apply validation chain: `tofu validate` + standard tools or custom commands |
| [Ignore Providers](doc/ignore-providers.md) | Strip source provider/backend blocks, inject custom provider config |
| [Blast Radius](doc/blast-radius.md) | Blast radius tracking and conditional auto-approve by threshold |
| [Revision History](doc/revisions.md) | Audit trail of every apply, rollback to any stored revision |
| [Scheduled Apply](doc/scheduled-apply.md) | Gate applies behind cron-based maintenance windows |
| [TTL Auto-Deletion](doc/ttl.md) | Automatically delete projects after a configured duration |
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
