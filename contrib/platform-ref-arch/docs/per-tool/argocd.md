# ArgoCD Integration

## Overview

ArgoCD provides GitOps continuous delivery. Users create `Application` resources in their kcp workspace pointing to a Git repository. ArgoCD on the physical cluster reconciles the desired state.

## Resources

| Resource | API Group | Scope | Notes |
|----------|-----------|-------|-------|
| Application | argoproj.io/v1alpha1 | Namespaced | Primary user-facing resource |
| AppProject | argoproj.io/v1alpha1 | Namespaced | Groups applications, defines access policies |
| ApplicationSet | argoproj.io/v1alpha1 | Namespaced | Generates Applications from templates |

## Cross-CRD References

- `Application.spec.project` -> `AppProject` (by name, same namespace)
- `ApplicationSet.spec.template.spec.project` -> `AppProject`

**Gap:** api-syncagent `related` only supports ConfigMap/Secret. AppProject must be synced alongside Application for reconciliation to succeed.

**Workaround:** Create a separate PublishedResource for AppProject. Both Application and AppProject sync independently. As long as the AppProject is created before the Application, ArgoCD will reconcile correctly. Users must be aware of this ordering requirement.

## Permission Claims

The ArgoCD APIExport needs permission claims for:
- `secrets` (get, list, watch) - Git repository credentials, cluster connection secrets
- `configmaps` (get, list, watch) - ArgoCD configuration, GPG keys

## Status Fields

ArgoCD Application status is rich and deeply nested:
- `.status.health` - application health status
- `.status.sync` - sync state (Synced, OutOfSync)
- `.status.operationState` - last operation details (internal, can be trimmed)
- `.status.resources` - list of managed resources with health
- `.status.conditions` - standard conditions

Recommended status mutations to reduce noise:
```yaml
mutation:
  status:
    - delete:
        path: ".status.operationState.syncResult.manifests"
    - delete:
        path: ".status.history"
```

## Physical Cluster Setup

ArgoCD must be installed in a dedicated namespace (e.g., `argocd`). Each tenant workspace maps to a namespace where ArgoCD Application objects are created. ArgoCD must be configured to watch all namespaces (or the specific tenant namespaces).

The `argocd-server` needs `--application-namespaces=*` to support applications in non-default namespaces.

## User Experience

```bash
# In kcp workspace root:tenants:team-alpha
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: default
spec:
  sourceRepos:
    - '*'
  destinations:
    - namespace: '*'
      server: '*'
EOF

kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: default
spec:
  project: default
  source:
    repoURL: https://github.com/example/app.git
    targetRevision: HEAD
    path: manifests
  destination:
    server: https://kubernetes.default.svc
    namespace: default
EOF

kubectl get applications
# NAME     SYNC     HEALTH   STATUS
# my-app   Synced   Healthy
```

## Known Limitations

1. **Destination server:** The `.spec.destination.server` field in ArgoCD Application points to a K8s cluster. In the kcp model, users don't know the physical cluster address. The api-syncagent should rewrite this field via PublishedResource mutations to point to the local cluster.
2. **ArgoCD UI:** Users cannot access the ArgoCD web UI since it runs on the physical cluster. A separate UI proxy or kcp-native dashboard would be needed.
3. **Notifications:** ArgoCD notifications controller runs on the physical cluster. Notification destinations (Slack webhooks, etc.) must be configured by the platform team, not per-tenant.
