# OpenTofu Kubernetes Operator

A Kubernetes operator that runs OpenTofu/Terraform declaratively via CRDs — plan/approve workflows, drift detection, rollback, and GitOps-native infrastructure management.

## How It Works

```
TofuProgram (HCL or git repo)  +  TofuProject (params, settings)
        |                                |
        +----------------+--------------+
                         v
         Operator renders generated files (backend, vars, providers) into a ConfigMap
         Git sources are cloned at runtime via init container — not stored in the ConfigMap
                         v
         Kubernetes Job runs `tofu plan` / `tofu apply`
                         v
         State stored in Kubernetes Secrets, outputs written to status
```

1. **`TofuProgram`** — define your infrastructure code (inline HCL or a git repo) and provider requirements
2. **`TofuProject`** — bind a program to an environment with parameters, approval settings, and scheduling
3. **The operator** — renders config, runs Jobs, tracks state, detects drift, and stores revision history

## Features

- Declarative OpenTofu execution via CRDs
- Kubernetes backend for state (Secrets)
- Reusable programs (`TofuProgram`) — inline HCL or [git sources](features/git-sources.md)
- Parameterized runs (`TofuProject`) — inline, `valuesFrom` (ordered layering), `paramFrom` (bulk ConfigMap/Secret), and `paramBindings` (individual key refs)
- Drift-safe: new Job created when inputs change (including ConfigMap/Secret watches)
- [Cross-project output dependencies](features/dependencies.md)
- [Provider plugin cache](features/provider-cache.md) via PVC
- [Plan-then-approve](features/plan-approve.md) workflow — via kubectl annotation or [GitHub PR approval](features/plan-approve.md#github-pr-based-approval)
- [Delete protection](features/delete-protection.md) — prevent accidental infrastructure destruction
- Configurable service account — custom SA or annotations (IRSA/workload identity)
- [kubectl plugin](operations/kubectl-plugin.md) — `kubectl tofu plan|approve|delete|suspend|resume|diff`
- Suspend mode — pause reconciliation entirely
- Sync interval — periodic re-reconciliation
- Automatic destroy via finalizer
- Leader election for HA deployments
- Job locking — one Job per project at a time
- Custom environment variables — inject env vars and envFrom into tofu Jobs
- [Extra volumes](features/extra-volumes.md) — mount ConfigMaps, Secrets, PVCs, or image volumes into tofu Jobs
- Resource limits — set CPU/memory requests and limits on Job containers
- Retry policy — automatically retry failed Jobs with configurable delay
- Drift detection — periodic plan-only jobs to detect infrastructure drift
- Webhook notifications — send HTTP POST notifications on lifecycle events
- [Validation chain](features/validation.md) — `tofu validate` + standard tools (tflint, checkov, trivy) or custom commands as init containers
- [Ignore providers & additional providers HCL](features/ignore-providers.md) — strip source provider/backend blocks, inject custom provider config
- [Blast radius tracking](features/blast-radius.md) — parsed plan counts with conditional auto-approve by threshold
- [Revision history & pinned revisions](features/revisions.md) — audit trail of every apply, rollback to any stored revision
- [Scheduled apply windows](features/scheduled-apply.md) — gate when approved plans are applied using cron-based maintenance windows
- [TTL auto-deletion](features/ttl.md) — automatically delete projects after a configured duration
