# Provider Plugin Cache

Configurable caching of OpenTofu provider plugins via PVC, so `tofu init` doesn't re-download providers on every Job.

```yaml
spec:
  cache:
    mode: shared          # "shared" or "dedicated"
    size: "2Gi"           # default: "1Gi"
    storageClass: "fast"  # optional
```

## Modes

| Mode | PVC | Locking |
|------|-----|---------|
| `shared` | One PVC per namespace (`tofu-plugin-cache`) | Jobs serialized namespace-wide |
| `dedicated` | One PVC per project (`{name}-plugin-cache`) | Per-project locking (default) |

When no `cache` is specified, behaviour is unchanged (no PVC, no caching).

The cache PVC is mounted at `/plugin-cache` and the `TF_PLUGIN_CACHE_DIR` environment variable is set automatically.

See [`examples/tofuproject-cached.yaml`](../examples/tofuproject-cached.yaml) for a complete example.
