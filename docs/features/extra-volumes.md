# Extra Volumes & Volume Mounts

Mount custom data (ConfigMaps, Secrets, PVCs, OCI image volumes) into tofu runner Jobs — useful for `local-exec` provisioners, external data sources, custom tools, or policy files.

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `spec.extraVolumes` | `[]corev1.Volume` | Additional volumes added to the Job pod |
| `spec.extraVolumeMounts` | `[]corev1.VolumeMount` | Additional volume mounts added to the `tofu` container |

Both fields use native Kubernetes types, consistent with how `env` and `envFrom` work.

## Examples

### ConfigMap volume

Mount OPA policy files for a `local-exec` provisioner:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: infra
spec:
  programRef:
    name: my-program
  extraVolumes:
    - name: policy-files
      configMap:
        name: opa-policies
  extraVolumeMounts:
    - name: policy-files
      mountPath: /policies
      readOnly: true
```

### Secret volume

Mount cloud credentials as a file:

```yaml
spec:
  extraVolumes:
    - name: cloud-creds
      secret:
        secretName: gcp-service-account
  extraVolumeMounts:
    - name: cloud-creds
      mountPath: /var/run/secrets/cloud
      readOnly: true
```

### OCI image volume

Mount a custom tool from a container image:

```yaml
spec:
  extraVolumes:
    - name: custom-tool
      image:
        reference: registry.example.com/my-tool:v1.0
  extraVolumeMounts:
    - name: custom-tool
      mountPath: /usr/local/bin/my-tool
      subPath: usr/bin/my-tool
```

### PersistentVolumeClaim

Mount shared data from a PVC:

```yaml
spec:
  extraVolumes:
    - name: shared-data
      persistentVolumeClaim:
        claimName: shared-infra-data
  extraVolumeMounts:
    - name: shared-data
      mountPath: /data
      readOnly: true
```

## Notes

- Extra volumes are appended to all Job types (plan, apply, destroy, drift detection, pinned revision).
- Built-in volumes (`tf-config`, `work`, `git-repo`, `plugin-cache`) are not affected.
- Volume names must not collide with built-in volume names.
