# Crossplane Integration

## Overview

Crossplane provides infrastructure provisioning through Kubernetes-native APIs. Platform teams define XRDs (CompositeResourceDefinitions) and Compositions on the physical cluster. Users interact only with Claims through their kcp workspace.

## Resources

| Resource | API Group | Scope | Notes |
|----------|-----------|-------|-------|
| Claims | Custom per XRD (e.g., `databases.example.org`) | Namespaced | User-facing, one per XRD |
| CompositeResourceDefinition | apiextensions.crossplane.io/v1 | Cluster | Platform team only (not exposed) |
| Composition | apiextensions.crossplane.io/v1 | Cluster | Platform team only (not exposed) |

Only Claims are exposed to users through kcp. XRDs and Compositions stay on the physical cluster, managed by the platform team.

## Design Pattern

Crossplane is uniquely suited to this architecture because Claims are already an abstraction layer:

```
User (kcp workspace)                    Physical Cluster
+-------------------+                   +-----------------------------------+
| Claim: Database   | --- sync --->     | Claim: Database                   |
|   spec:           |                   |   -> XR: XDatabase                |
|     engine: pg    |                   |      -> ManagedResource: RDSInstance
|     size: small   |                   |      -> ManagedResource: SecurityGroup
+-------------------+                   +-----------------------------------+
                    <--- status ---      | Status: Ready, connectionDetails |
```

## Cross-CRD References

- `Claim` -> `ProviderConfig` (implicit via Composition, not user-specified)
- `Claim` -> connection details `Secret` (output, synced back to kcp)

Connection details Secrets are the primary cross-resource concern. api-syncagent's existing `related` mechanism supports Secrets, so this works today.

## Permission Claims

The Crossplane APIExport needs:
- `secrets` (get, list, watch, create, update) - connection details Secrets must be created in the consumer workspace

## Status Fields

Crossplane Claim status:
- `.status.conditions` - Ready, Synced (standard Crossplane conditions)
- `.status.connectionDetails.lastPublishedTime` - when connection details were last published

Recommended: expose status as-is. Crossplane's condition model is already clean.

## Dynamic API Challenge

**Gap:** Crossplane generates CRDs dynamically from XRDs. When the platform team creates a new XRD:

1. Crossplane creates a CRD on the physical cluster (e.g., `databases.example.org`)
2. Someone must create an `APIResourceSchema` in kcp from that CRD's schema
3. Someone must update the `APIExport` to include the new resource
4. Someone must create a `PublishedResource` for api-syncagent

This is currently a manual process that must be repeated for every new XRD.

**Proposed solution:** A CRD Discovery Controller (see gap-analysis.md, Gap 4) that watches for Crossplane-generated CRDs (identifiable by labels `crossplane.io/composite`) and automates the kcp-side object creation.

## Physical Cluster Setup

Install Crossplane with required providers:

```bash
helm install crossplane crossplane-stable/crossplane --namespace crossplane-system

# Install providers (e.g., provider-aws)
kubectl apply -f - <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws
spec:
  package: xpkg.upbound.io/upbound/provider-aws-s3:v1.0.0
EOF

# Create XRDs and Compositions (platform team responsibility)
kubectl apply -f platform/xrds/
kubectl apply -f platform/compositions/
```

## User Experience

```bash
# In kcp workspace root:tenants:team-alpha
kubectl apply -f - <<EOF
apiVersion: example.org/v1alpha1
kind: Database
metadata:
  name: my-db
  namespace: default
spec:
  engine: postgresql
  size: small
  region: us-east-1
EOF

kubectl get databases
# NAME    SYNCED   READY   AGE
# my-db   True     True    5m

# Connection details Secret created automatically
kubectl get secret my-db-connection -o jsonpath='{.data.endpoint}' | base64 -d
# my-db.abc123.us-east-1.rds.amazonaws.com
```

## Known Limitations

1. **Provider configuration:** Users cannot create or modify `ProviderConfig` resources. Cloud credentials are managed by the platform team on the physical cluster. This is intentional - users should not manage cloud credentials.
2. **Composition selection:** If multiple Compositions match a Claim, the platform team must ensure deterministic selection (via `compositionSelector` or `compositionRef` in the XRD). Users cannot choose compositions unless the Claim schema exposes a field for it.
3. **Managed resource visibility:** Users cannot see the underlying ManagedResources (RDS instances, S3 buckets). They only see the Claim status. This is intentional but may frustrate debugging.
4. **Schema updates:** When the platform team updates an XRD, the corresponding APIResourceSchema in kcp must be recreated (APIResourceSchemas are immutable). The CRD Discovery Controller would handle this automatically.
