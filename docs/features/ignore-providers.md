# Ignore Providers & Additional Providers HCL

When using git-sourced TofuPrograms, the source `.tf` files often contain hardcoded `provider` configurations (e.g., AWS region, credentials) and `backend` blocks (e.g., S3 state) that conflict with the operator's managed state and the user's desired provider setup.

## Backend Stripping (Always On)

The operator automatically strips all `backend "..." { ... }` blocks from source `.tf` files before running `tofu init`. This ensures the operator's Kubernetes backend always takes effect, regardless of what backend the source code declares.

No configuration is needed — this happens for every TofuProject.

## `spec.ignoreProviders`

When set to `true`, all `provider "name" { ... }` blocks are stripped from source `.tf` files. This is useful when:

- The git source declares provider configuration (regions, credentials) that differs from your cluster's setup
- You want the operator-managed `providers.tf` (from `TofuProgram.spec.providers`) to be the sole provider configuration
- You combine git sources with `additionalProvidersHCL` for full control

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production
spec:
  programRef:
    name: infra-from-git
  autoApprove: true
  ignoreProviders: true
```

## `spec.additionalProvidersHCL`

Raw HCL written as `additional-providers.tf` into the working directory. Use this to provide replacement provider configuration when stripping the original providers.

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production
spec:
  programRef:
    name: infra-from-git
  autoApprove: true
  ignoreProviders: true
  additionalProvidersHCL: |
    provider "aws" {
      region = "eu-central-1"
      assume_role {
        role_arn = "arn:aws:iam::123456789012:role/tofu-runner"
      }
    }
```

## Combining Both Features

A typical pattern for git-sourced programs:

1. The `TofuProgram` declares `spec.providers` with `required_providers` (source + version constraints)
2. The `TofuProject` sets `ignoreProviders: true` to strip hardcoded provider blocks from the git source
3. The `TofuProject` sets `additionalProvidersHCL` with the cluster-specific provider configuration

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: infra-from-git
spec:
  source:
    url: https://github.com/org/infra.git
    ref: main
  providers:
    - name: aws
      source: hashicorp/aws
      version: "~> 5.0"
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production
spec:
  programRef:
    name: infra-from-git
  autoApprove: true
  ignoreProviders: true
  additionalProvidersHCL: |
    provider "aws" {
      region = "eu-central-1"
    }
```

## How It Works

The operator injects shell commands into the Job container that run before `tofu init`:

1. Copy source files to `/work/`
2. Strip `backend` blocks from all `.tf` files (always)
3. Strip `provider` blocks from all `.tf` files (when `ignoreProviders: true`)
4. Write `additional-providers.tf` from ConfigMap (when `additionalProvidersHCL` is set)
5. Run `tofu init` and apply/plan/destroy

Generated files (`backend.tf`, `providers.tf`, `additional-providers.tf`) are never modified by the stripping steps.
