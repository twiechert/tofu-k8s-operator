# Git Repository Source

Instead of inline HCL, you can point a `TofuProgram` at a git repository containing `.tf` files.

## Example: Public Repo

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

## Example: Private Repo (PAT / GitHub App Token)

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

## Example: Private Repo (SSH Deploy Key)

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

## How It Works

1. An `alpine/git` init container clones the repo (with credentials if provided)
2. The repo contents (or subdirectory) are copied into the working directory
3. The operator overlays `backend.tf`, `terraform.tfvars.json`, and optionally `providers.tf` on top
4. `tofu init && tofu apply` runs as usual

`programHCL` and `source` are mutually exclusive — the controller validates that exactly one is set.
