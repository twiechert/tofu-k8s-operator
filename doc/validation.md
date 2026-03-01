# Validation

Run validation logic before `tofu apply` to catch issues early. The operator supports two layers of validation:

1. **`tofu validate`** — runs inside the main container after `tofu init`, before plan/apply/destroy. Enabled by default.
2. **Validation steps** — optional init containers running standard tools (tflint, checkov, trivy) or custom commands. Each step runs in its own container and fails fast before the main container starts.

```yaml
spec:
  validation:
    tofuValidate: true   # default: true
    steps:
      - name: tflint
        standard: tflint
      - name: my-policy
        custom:
          command: "conftest test --policy /policies ."
          image: openpolicyagent/conftest:latest
```

## Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tofuValidate` | boolean | `true` | Run `tofu validate` after `tofu init` in the main container |
| `steps` | array | `[]` | List of validation steps to run as init containers |
| `steps[].name` | string | (required) | Name of the validation step (used as init container name) |
| `steps[].standard` | string | — | Use a built-in validator: `tflint`, `checkov`, or `trivy` |
| `steps[].custom.command` | string | (required) | Shell command to run |
| `steps[].custom.image` | string | main tofu image | Container image for the custom step |

Each step must set exactly one of `standard` or `custom`.

## Standard Validators

Built-in validators with preconfigured images and commands:

| Name | Image | Command |
|------|-------|---------|
| `tflint` | `ghcr.io/terraform-linters/tflint:latest` | `tflint --init && tflint` |
| `checkov` | `bridgecrew/checkov:latest` | `checkov -d . --framework terraform --compact` |
| `trivy` | `aquasec/trivy:latest` | `trivy config .` |

```yaml
spec:
  validation:
    steps:
      - name: tflint
        standard: tflint
      - name: security-scan
        standard: checkov
```

## Custom Validators

Run any tool by providing a command and optionally a container image. If no image is specified, the main tofu image is used.

```yaml
spec:
  validation:
    steps:
      - name: conftest
        custom:
          command: "conftest test --policy /policies ."
          image: openpolicyagent/conftest:latest
      - name: custom-check
        custom:
          command: "grep -r 'encryption' *.tf || echo 'WARNING: no encryption found'"
```

## How It Works

### tofu validate

When `tofuValidate` is `true` (the default), the generated shell script includes `tofu validate` between `tofu init` and the main command (plan/apply/destroy). This validates the configuration syntax and provider requirements after providers have been initialized.

To disable it:

```yaml
spec:
  validation:
    tofuValidate: false
```

### Validation Steps (Init Containers)

Each validation step becomes a Kubernetes init container on the Job pod. Init containers run sequentially before the main container and in the order they are defined:

1. **git-clone** (if git source mode) — clones the repository
2. **validate-\<name\>** — one per validation step, in order
3. **main container** — runs `tofu init`, validate, plan/apply/destroy

Each init container:
- Copies source files to a writable `/tmp/validate` directory
- Mounts `/tf-config` (read-only) for generated config (backend.tf, tfvars, etc.)
- In git mode, also mounts `/git-repo` (read-only)
- Runs the tool command inside `/tmp/validate`
- Uses `set -euo pipefail` — any non-zero exit code fails the Job

### Hash Computation

Changes to the validation configuration (adding/removing steps, changing commands or images, toggling `tofuValidate`) are included in the spec hash. This means changing validation settings triggers a new Job, just like changing params or source code.

## Disabling tofu validate

If your configuration relies on external modules or has intentional validation warnings, you can disable the built-in `tofu validate`:

```yaml
spec:
  validation:
    tofuValidate: false
```

Existing projects without a `validation` field work unchanged — `tofu validate` runs by default and no validation steps are added.

## Example

Full example with multiple validation layers:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: production-vpc
spec:
  programRef:
    name: vpc-module
  autoApprove: true
  validation:
    tofuValidate: true
    steps:
      - name: lint
        standard: tflint
      - name: security
        standard: checkov
      - name: iac-scan
        standard: trivy
      - name: org-policy
        custom:
          command: "conftest test --policy /policies ."
          image: openpolicyagent/conftest:latest
  params:
    environment: production
    cidr_block: "10.0.0.0/16"
```

This runs tflint, checkov, trivy, and a custom conftest policy check as init containers, then `tofu validate` inside the main container, all before `tofu apply`.
