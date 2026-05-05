# Flux Integration

## Overview

Flux provides GitOps toolkit for Kubernetes. Users create source resources (GitRepository, HelmRepository) and reconciliation resources (Kustomization, HelmRelease) in their kcp workspace.

## Resources

| Resource | API Group | Scope | Notes |
|----------|-----------|-------|-------|
| GitRepository | source.toolkit.fluxcd.io/v1 | Namespaced | Git source |
| OCIRepository | source.toolkit.fluxcd.io/v1beta2 | Namespaced | OCI artifact source |
| HelmRepository | source.toolkit.fluxcd.io/v1 | Namespaced | Helm chart repository |
| Kustomization | kustomize.toolkit.fluxcd.io/v1 | Namespaced | Kustomize reconciler |
| HelmRelease | helm.toolkit.fluxcd.io/v2 | Namespaced | Helm chart reconciler |

## Cross-CRD References

Flux is heavily cross-referenced:

- `Kustomization.spec.sourceRef` -> `GitRepository` or `OCIRepository` (by kind + name)
- `HelmRelease.spec.chart.spec.sourceRef` -> `HelmRepository` (by kind + name)
- `HelmRelease.spec.valuesFrom` -> `ConfigMap` or `Secret` (by kind + name)
- `Kustomization.spec.dependsOn` -> other `Kustomization` resources (by name)

**Gap:** Cross-CRD reference sync is critical for Flux. A `Kustomization` without its `GitRepository` will never reconcile.

**Workaround:** Since all Flux source types are synced via their own PublishedResources, both source and consumer objects are synced independently. The physical cluster Flux controllers will retry reconciliation until the source appears. This works but may cause transient error status during initial sync.

## Permission Claims

The Flux APIExport needs permission claims for:
- `secrets` (get, list, watch) - Git SSH keys, Helm registry credentials, decryption keys
- `configmaps` (get, list, watch) - Helm values, Kustomize patches

## Status Fields

Flux uses a standardized status pattern across all resources:
- `.status.conditions` - Ready, Reconciling, Stalled, HealthCheckSuccess
- `.status.lastAppliedRevision` - last applied Git commit or chart version
- `.status.lastAttemptedRevision` - last attempted revision
- `.status.artifact` - source artifact details (internal, can be trimmed)

Recommended status mutations:
```yaml
mutation:
  status:
    - delete:
        path: ".status.artifact.digest"
    - delete:
        path: ".status.artifact.metadata"
```

## Physical Cluster Setup

Flux controllers must be installed (flux-source-controller, flux-kustomize-controller, flux-helm-controller). Configure multi-tenancy mode:

```bash
flux install --components=source-controller,kustomize-controller,helm-controller \
  --watch-all-namespaces=true
```

## User Experience

```bash
# In kcp workspace root:tenants:team-alpha
kubectl apply -f - <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: my-app
  namespace: default
spec:
  interval: 5m
  url: https://github.com/example/app.git
  ref:
    branch: main
EOF

kubectl apply -f - <<EOF
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app
  namespace: default
spec:
  interval: 5m
  sourceRef:
    kind: GitRepository
    name: my-app
  path: ./deploy
  prune: true
EOF

kubectl get kustomizations
# NAME     AGE   READY   STATUS
# my-app   30s   True    Applied revision: main@sha1:abc123
```

## Known Limitations

1. **Target namespace:** Flux Kustomization has `.spec.targetNamespace` which controls where manifests are applied on the physical cluster. In the kcp model, the target is always the tenant's namespace. The api-syncagent should either lock this field or rewrite it.
2. **Cross-namespace references:** Flux supports `spec.sourceRef.namespace` for cross-namespace source references. In the kcp model, all resources are in the same workspace-mapped namespace, so cross-namespace refs should be blocked.
3. **Flux CLI:** `flux get` and other CLI commands work against the kcp workspace API, but `flux logs` requires access to the physical cluster's flux controllers.
