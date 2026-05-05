# KRO (Kubernetes Resource Orchestrator) Integration

## Overview

KRO allows platform teams to define compositions of Kubernetes resources via `ResourceGroup` CRDs. KRO generates instance CRDs that users interact with. Similar to Crossplane but focused on Kubernetes-native resources rather than cloud infrastructure.

## Resources

| Resource | API Group | Scope | Notes |
|----------|-----------|-------|-------|
| ResourceGroup | kro.run/v1alpha1 | Cluster | Platform team only (defines compositions) |
| Generated instances | Custom per ResourceGroup | Namespaced | User-facing, one per ResourceGroup |

Only generated instance CRDs are exposed to users through kcp. ResourceGroups stay on the physical cluster.

## Design Pattern

```
Platform Team (physical cluster)        User (kcp workspace)
+-----------------------------+         +-------------------+
| ResourceGroup: WebApp       |         | WebApp:           |
|   defines: Deployment,      |  sync   |   name: my-app    |
|   Service, Ingress,         | <-----> |   image: nginx    |
|   HPA from a single spec    |         |   replicas: 3     |
+-----------------------------+         +-------------------+
```

KRO generates a CRD (e.g., `webapps.kro.run`) from the ResourceGroup definition. Users create instances of this CRD.

## Cross-CRD References

Minimal. Generated instance CRDs are self-contained - the ResourceGroup definition handles all internal references. Users only interact with the instance CRD.

## Dynamic API Challenge

**Same as Crossplane.** KRO generates CRDs from ResourceGroup definitions. Each new ResourceGroup produces a new CRD that must be:
1. Extracted into an APIResourceSchema in kcp
2. Added to the APIExport
3. Configured as a PublishedResource for api-syncagent

**Proposed solution:** The CRD Discovery Controller (see gap-analysis.md, Gap 4) watches for KRO-generated CRDs and automates kcp-side object creation. KRO-generated CRDs can be identified by labels or ownership references to ResourceGroup objects.

## Permission Claims

The KRO APIExport needs:
- `configmaps` (get, list, watch) - for resource templates
- `secrets` (get, list, watch) - for sensitive configuration

## Status Fields

KRO instance status typically includes:
- `.status.conditions` - Ready, Progressing
- `.status.resources` - list of managed sub-resources with their status

## Physical Cluster Setup

```bash
# Install KRO
kubectl apply -f https://raw.githubusercontent.com/kro-run/kro/main/install.yaml

# Create ResourceGroups (platform team)
kubectl apply -f - <<EOF
apiVersion: kro.run/v1alpha1
kind: ResourceGroup
metadata:
  name: webapp
spec:
  schema:
    apiVersion: v1alpha1
    kind: WebApp
    spec:
      image: string
      replicas: integer | default=1
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: \${schema.metadata.name}
        spec:
          replicas: \${schema.spec.replicas}
          ...
    - id: service
      template:
        apiVersion: v1
        kind: Service
        ...
EOF
```

## User Experience

```bash
# In kcp workspace root:tenants:team-alpha
kubectl apply -f - <<EOF
apiVersion: kro.run/v1alpha1
kind: WebApp
metadata:
  name: my-app
  namespace: default
spec:
  image: nginx:latest
  replicas: 3
EOF

kubectl get webapps
# NAME     READY   AGE
# my-app   True    2m
```

## Known Limitations

1. **ResourceGroup management:** Users cannot create or modify ResourceGroups - they are platform team resources. This is intentional.
2. **Schema evolution:** When a ResourceGroup is updated, KRO regenerates the CRD. The APIResourceSchema in kcp must be recreated to match.
3. **Sub-resource visibility:** Users cannot see the Deployments, Services, etc. created by KRO. They only see the instance status.
