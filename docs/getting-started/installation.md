# Installation

## Helm (recommended)

```bash
helm install tofu-k8s-operator oci://ghcr.io/twiechert/charts/tofu-k8s-operator \
  --version 0.9.0 \
  --namespace tofu-system \
  --create-namespace
```

## Raw manifests

```bash
kubectl apply -k deploy/
```

## kubectl Plugin

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

See [kubectl Plugin](../operations/kubectl-plugin.md) for the full command reference.
