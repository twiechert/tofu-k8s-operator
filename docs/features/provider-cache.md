# Provider & Module Cache

Configurable caching of OpenTofu provider plugins and modules via PVC, so `tofu init` doesn't re-download them on every Job.

```yaml
spec:
  cache:
    mode: shared          # "shared" or "dedicated"
    size: "2Gi"           # default: "1Gi"
    storageClass: "fast"  # optional
    modules: true         # also cache downloaded modules (default: false)
```

## Modes

| Mode | PVC | Locking |
|------|-----|---------|
| `shared` | One PVC per namespace (`tofu-plugin-cache`) | Jobs serialized namespace-wide |
| `dedicated` | One PVC per project (`{name}-plugin-cache`) | Per-project locking (default) |

When no `cache` is specified, behaviour is unchanged (no PVC, no caching).

## Provider Cache

The cache PVC is mounted at `/plugin-cache` and the `TF_PLUGIN_CACHE_DIR` environment variable is set automatically. Providers are stored under the `providers` subPath on the PVC.

## Module Cache

When `modules: true` is set, downloaded Terraform/OpenTofu modules are cached across jobs using the `modules` subPath on the same PVC. This is particularly useful for projects that reference many remote modules, avoiding re-downloads on every plan/apply cycle.

Both provider and module caches share the same PVC, organized by subPath:

```
PVC
├── providers/    # TF_PLUGIN_CACHE_DIR
└── modules/      # .terraform/modules
```
