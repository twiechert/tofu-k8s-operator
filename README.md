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
- Reusable programs (`TofuProgram`)
- Parameterized runs (`TofuProject`)
- Drift-safe: new Job created when inputs change
- **Suspend mode** — pause reconciliation entirely
- **Plan-then-approve** — review `tofu plan` output before applying
- **kubectl plugin** — `kubectl tofu plan|approve|suspend|resume`

## CRDs

### TofuProgram
Defines:
- Inline OpenTofu HCL
- Required providers

### TofuProject
Defines:
- Reference to a program
- Arbitrary parameters → `terraform.tfvars.json`
- Backend configuration
- Execution settings

## Testing

Unit tests cover:
- providers.tf rendering
- backend.tf rendering
- job command generation
- hash stability

Run unit tests:
```bash
just test
```

Run with coverage:
```bash
just test-cover
```

Run end-to-end tests (requires Docker; spins up a Kind cluster):
```bash
just e2e
```

## Build

```bash
just build          # compile the binary
just docker-build   # build the container image
```

## Git Repository Source

Instead of inline HCL, you can point a `TofuProgram` at a git repository containing `.tf` files.

### Example: Public Repo

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: vpc-from-git
  namespace: default
spec:
  source:
    url: "https://github.com/example/terraform-modules.git"
    ref: "v1.2.0"          # branch, tag, or SHA (default: "main")
    path: "modules/vpc"     # subdirectory (default: repo root)
```

### Example: Private Repo (PAT / GitHub App Token)

Create a Secret with a `token` key:

```bash
kubectl create secret generic git-creds \
  --from-literal=token=ghp_xxxxxxxxxxxxxxxxxxxx
```

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: private-module
  namespace: default
spec:
  source:
    url: "https://github.com/myorg/private-infra.git"
    ref: main
    credentialsSecretRef:
      name: git-creds
```

### Example: Private Repo (SSH Deploy Key)

Create a Secret with `sshPrivateKey` (and optionally `known_hosts`):

```bash
kubectl create secret generic git-ssh-creds \
  --from-file=sshPrivateKey=~/.ssh/deploy_key \
  --from-file=known_hosts=~/.ssh/known_hosts
```

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: ssh-module
  namespace: default
spec:
  source:
    url: "git@github.com:myorg/private-infra.git"
    ref: main
    credentialsSecretRef:
      name: git-ssh-creds
```

### How It Works

1. An `alpine/git` init container clones the repo (with credentials if provided)
2. The repo contents (or subdirectory) are copied into the working directory
3. The operator overlays `backend.tf`, `terraform.tfvars.json`, and optionally `providers.tf` on top
4. `tofu init && tofu apply` runs as usual

`programHCL` and `source` are mutually exclusive — the controller validates that exactly one is set.

## Example: AWS S3 Bucket

Below is a realistic example using the AWS provider to provision an S3 bucket.

### TofuProgram (AWS S3)
```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
	name: s3-bucket
	namespace: default
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

		resource "aws_s3_bucket" "bucket" {
			bucket = var.bucket_name
			force_destroy = true
		}

		output "bucket_arn" {
			value = aws_s3_bucket.bucket.arn
		}
```

### TofuProject (AWS S3)
```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
	name: s3-bucket-demo
	namespace: default
spec:
	programRef:
		name: s3-bucket
	params:
		aws_region: "us-west-2"
		bucket_name: "my-demo-bucket-12345"
	backend:
		secretSuffix: "s3-bucket-demo"
		namespace: "default"
	autoApprove: true
```

**Note:**
- You must provide AWS credentials (e.g., via a Kubernetes Secret and projected environment variables) for the operator Job to authenticate with AWS.
- The bucket name must be globally unique.

## Deployment

### Helm (recommended)

```bash
helm install tofu-operator ./charts/tofu-operator \
  --namespace tofu-system \
  --create-namespace
```

### Raw manifests

```bash
kubectl apply -k deploy/
```

## Sync Interval

Set `spec.syncInterval` to periodically re-reconcile after a successful sync. This is useful for picking up changes from referenced resources (e.g., TofuProgram edits, git branch tip updates) without waiting for a watch event.

```yaml
spec:
  syncInterval: "10m"   # re-reconcile every 10 minutes
```

If omitted or empty, the controller only reconciles on spec/status changes (the default behaviour). The value must be a valid Go duration string (e.g. `"5m"`, `"1h"`, `"30s"`).

## Suspend Mode

Set `spec.suspend: true` to pause reconciliation. No new Jobs will be created while suspended.

```yaml
spec:
  suspend: true
```

Resume with:
```bash
kubectl tofu resume <project>
```

Or patch directly:
```bash
kubectl patch tofuproject <name> --type=merge -p '{"spec":{"suspend":false}}'
```

## Plan-Then-Approve Workflow

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

## kubectl Plugin

Install:
```bash
just build-plugin
cp bin/kubectl-tofu /usr/local/bin/   # or anywhere on your PATH
```

Or via `go install`:
```bash
just install-plugin
```

Commands:

| Command | Description |
|---------|-------------|
| `kubectl tofu plan <project> [-n ns]` | Show plan output and status |
| `kubectl tofu approve <project> [-n ns]` | Approve a pending plan |
| `kubectl tofu suspend <project> [-n ns]` | Pause reconciliation |
| `kubectl tofu resume <project> [-n ns]` | Resume reconciliation |

## Cross-Project Output Dependencies

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

**How it works:**
1. The controller resolves each dependency by fetching the upstream TofuProject's `status.outputs`
2. Upstream output values are merged into the downstream project's effective params (overriding `spec.params` on conflict)
3. If any upstream is not `Succeeded` or is missing a required output, the downstream enters `WaitingDependency` phase and requeues after 15s
4. Changes to upstream outputs are detected via a cross-project watch, triggering immediate re-reconciliation of dependents

See `examples/tofuproject-dependency.yaml` for a complete example.

## Provider Plugin Cache

Configurable caching of OpenTofu provider plugins via PVC, so `tofu init` doesn't re-download providers on every Job.

```yaml
spec:
  cache:
    mode: shared          # "shared" or "dedicated"
    size: "2Gi"           # default: "1Gi"
    storageClass: "fast"  # optional
```

**Modes:**

| Mode | PVC | Locking |
|------|-----|---------|
| `shared` | One PVC per namespace (`tofu-plugin-cache`) | Jobs serialized namespace-wide |
| `dedicated` | One PVC per project (`{name}-plugin-cache`) | Per-project locking (default) |

When no `cache` is specified, behaviour is unchanged (no PVC, no caching).

The cache PVC is mounted at `/plugin-cache` and the `TF_PLUGIN_CACHE_DIR` environment variable is set automatically.

See `examples/tofuproject-cached.yaml` for a complete example.

## Operational Features

- **Leader election** (`--leader-elect`) for HA deployments
- **Automatic destroy** — deleting a TofuProject runs `tofu destroy` via a finalizer before the resource is removed
- **Job locking** — only one apply/destroy Job runs per project at a time; spec changes while a Job is active are queued until it completes

PRs welcome.
