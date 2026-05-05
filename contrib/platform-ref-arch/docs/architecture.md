# Architecture

## Personas

### Platform Team
- Manages kcp instance and physical clusters
- Installs and configures operators (ArgoCD, Flux, Crossplane, KRO)
- Creates provider workspaces with APIExports
- Deploys and configures api-syncagent on physical clusters
- Defines WorkspaceTypes and tenant structure

### Application Team
- Gets an `application`-type workspace
- Creates resources (Applications, Kustomizations, Claims) in their workspace
- Sees status synced back from physical clusters
- Never accesses physical clusters directly

## Workspace Hierarchy

```
root
 +-- platform-providers
 |    |
 |    +-- argocd-provider
 |    |     APIResourceSchema: v1alpha1.applications.argoproj.io
 |    |     APIResourceSchema: v1alpha1.appprojects.argoproj.io
 |    |     APIResourceSchema: v1alpha1.applicationsets.argoproj.io
 |    |     APIExport: argocd.argoproj.io
 |    |       resources: [applications, appprojects, applicationsets]
 |    |       permissionClaims: [secrets(get,list,watch), configmaps(get,list,watch)]
 |    |
 |    +-- flux-provider
 |    |     APIResourceSchema: v1.gitrepositories.source.toolkit.fluxcd.io
 |    |     APIResourceSchema: v1.ocirepositories.source.toolkit.fluxcd.io
 |    |     APIResourceSchema: v1.helmrepositories.source.toolkit.fluxcd.io
 |    |     APIResourceSchema: v1.kustomizations.kustomize.toolkit.fluxcd.io
 |    |     APIResourceSchema: v1.helmreleases.helm.toolkit.fluxcd.io
 |    |     APIExport: flux.toolkit.fluxcd.io
 |    |
 |    +-- crossplane-provider
 |    |     APIResourceSchema: (per XRD claim type, e.g. v1alpha1.databases.example.org)
 |    |     APIExport: crossplane.io
 |    |
 |    +-- kro-provider
 |    |     APIResourceSchema: v1alpha1.resourcegroups.kro.run
 |    |     APIResourceSchema: (per generated instance CRD)
 |    |     APIExport: kro.run
 |    |
 |    +-- argo-workflows-provider
 |          APIResourceSchema: v1alpha1.workflows.argoproj.io
 |          APIResourceSchema: v1alpha1.workflowtemplates.argoproj.io
 |          APIResourceSchema: v1alpha1.cronworkflows.argoproj.io
 |          APIExport: workflows.argoproj.io
 |
 +-- tenants
      |
      +-- team-alpha    (WorkspaceType: application)
      |     APIBinding -> argocd.argoproj.io
      |     APIBinding -> flux.toolkit.fluxcd.io
      |
      +-- team-beta     (WorkspaceType: application)
            APIBinding -> crossplane.io
            APIBinding -> kro.run
            APIBinding -> workflows.argoproj.io
```

## Physical Cluster Mapping

Each physical cluster runs:
1. One or more operators (ArgoCD, Flux, Crossplane, etc.)
2. An api-syncagent instance configured with PublishedResource objects
3. The agent connects to kcp using a kubeconfig scoped to the provider workspace

### Namespace Isolation

Each kcp workspace maps to a dedicated namespace on the physical cluster:

```
kcp workspace: root:tenants:team-alpha  (cluster name: 1abc2def3ghi)
    |
    v
physical cluster namespace: 1abc2def3ghi  (default naming: {{ .ClusterName }})
```

All resources from that workspace are synced into this single namespace, providing:
- Strong tenant isolation via K8s RBAC and NetworkPolicy
- ResourceQuota enforcement per tenant
- No cross-tenant resource visibility

### Multi-Cluster Topology

```
                    kcp
                   / | \
                  /  |  \
                 v   v   v
         cluster-a  cluster-b  cluster-c
         (ArgoCD)   (Crossplane)(Argo WF)
         (Flux)     (KRO)

Each cluster runs api-syncagent watching the same
provider workspace APIExport virtual workspace.

Workspace -> cluster assignment is currently manual
(api-syncagent runs per-cluster and watches all workspaces
via the APIExport virtual workspace URL).
```

## APIExport / APIBinding Flow

### Provider Setup (one-time)

```
1. Platform team creates APIResourceSchema from tool's CRD
   (extract OpenAPI schema, create immutable schema object)

2. Platform team creates APIExport referencing the schemas
   - Defines permission claims (what extra resources the agent needs)
   - kcp auto-generates identity secret + hash
   - kcp auto-creates APIExportEndpointSlice with virtual workspace URLs

3. api-syncagent on physical cluster uses the APIExport's
   virtual workspace URL to watch all consumer workspaces
```

### Consumer Binding (per workspace)

```
1. User creates workspace with type: application
   (or manually creates APIBinding)

2. WorkspaceType defaultAPIBindings creates APIBinding automatically

3. kcp resolves APIBinding -> APIExport
   - Generates CRDs in consumer workspace from APIResourceSchema
   - Consumer sees new API types (e.g., `applications.argoproj.io`)

4. User creates resources using bound APIs
   - Resources stored in kcp's etcd under APIExport identity prefix
   - Visible to api-syncagent via virtual workspace
```

## Security Model

### Workspace Isolation
- Each workspace is a separate API surface with its own RBAC
- Users in one workspace cannot see resources in another
- Service accounts are workspace-scoped

### Permission Claims
- APIExports declare what extra permissions they need (e.g., read Secrets)
- Consumers must explicitly accept or reject each claim
- Claims can be scoped via label selectors (not blanket access)

### Physical Cluster Isolation
- Per-workspace namespaces with K8s RBAC
- api-syncagent service account has scoped permissions
- NetworkPolicy can isolate tenant namespaces
- ResourceQuota limits per namespace

### Maximal Permission Policy
- APIExport can set upper bounds on consumer permissions
- Even if a consumer's workspace RBAC allows something, the APIExport can restrict it
